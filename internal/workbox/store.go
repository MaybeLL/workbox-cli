package workbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	_ "modernc.org/sqlite"
)

type store struct{ path string }

func configPath() (string, *Failure) {
	root := os.Getenv("WORKBOX_CONFIG_HOME")
	if root == "" {
		root = os.Getenv("XDG_CONFIG_HOME")
		if root == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", ioFailure("Could not locate configuration home")
			}
			root = filepath.Join(home, ".config")
		}
		root = filepath.Join(root, "workbox")
	}
	path, e := absolute(root)
	if e != nil {
		return "", e
	}
	return filepath.Join(path, "config.db"), nil
}
func absolute(raw string) (string, *Failure) {
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ioFailure("Could not locate user home")
		}
		if raw == "~" {
			raw = home
		} else {
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
	}
	p, err := filepath.Abs(raw)
	if err != nil {
		return "", fail("PATH_INVALID", "Could not resolve path")
	}
	return p, nil
}
func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
func dbError(err error) *Failure {
	if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "SQLITE_LOCKED") {
		return fail("CONFIG_CONFLICT", "Configuration database is busy")
	}
	return ioFailure("Could not read or write configuration database")
}
func (s store) open(write bool) (*sql.DB, *Failure) {
	mode := "ro"
	if write {
		mode = "rwc"
		if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
			return nil, ioFailure("Could not access configuration directory")
		}
	}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(5000)"}}
	if write {
		q.Set("_txlock", "immediate")
	}
	u := url.URL{Scheme: "file", Path: s.path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, dbError(err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func schema(ctx context.Context, tx *sql.Tx) *Failure {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return dbError(err)
	}
	if version != 1 {
		return fail("SCHEMA_UNSUPPORTED", "Unsupported database schema version")
	}
	return nil
}
func readSnapshot(ctx context.Context, tx *sql.Tx) (snapshot, *Failure) {
	s := emptySnapshot()
	rows, err := tx.QueryContext(ctx, "SELECT id,name,source_url,default_branch FROM repo_config ORDER BY name")
	if err != nil {
		return s, dbError(err)
	}
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &r.Branch); err != nil {
			rows.Close()
			return s, dbError(err)
		}
		s.Repos[r.Name] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, dbError(err)
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,name,repos,init_script FROM workspace_config ORDER BY name")
	if err != nil {
		return s, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w workspaceRow
		var raw string
		if err := rows.Scan(&w.ID, &w.Name, &raw, &w.Script); err != nil {
			return s, dbError(err)
		}
		if json.Unmarshal([]byte(raw), &w.Repos) != nil || w.Repos == nil {
			return s, invalid("Invalid repository references in database")
		}
		s.Workspaces[w.Name] = w
	}
	if err := rows.Err(); err != nil {
		return s, dbError(err)
	}
	return s, nil
}
func (s store) snapshot(ctx context.Context) (snapshot, *Failure) {
	exists, err := pathExists(s.path)
	if err != nil {
		return snapshot{}, ioFailure("Could not inspect configuration database")
	}
	if !exists {
		return emptySnapshot(), nil
	}
	db, e := s.open(false)
	if e != nil {
		return snapshot{}, e
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return snapshot{}, dbError(err)
	}
	defer tx.Rollback()
	if e := schema(ctx, tx); e != nil {
		return snapshot{}, e
	}
	return readSnapshot(ctx, tx)
}
func (s store) mutate(ctx context.Context, expected snapshot, action func(*sql.Tx, snapshot) *Failure) *Failure {
	existed, err := pathExists(s.path)
	if err != nil {
		return ioFailure("Could not inspect configuration database")
	}
	db, e := s.open(true)
	if e != nil {
		return e
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	var version, count int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return dbError(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&count); err != nil {
		return dbError(err)
	}
	if !existed && version == 0 && count == 0 {
		for _, ddl := range []string{"CREATE TABLE repo_config (id TEXT PRIMARY KEY NOT NULL, name TEXT UNIQUE NOT NULL, source_url TEXT NOT NULL, default_branch TEXT NOT NULL)", "CREATE TABLE workspace_config (id TEXT PRIMARY KEY NOT NULL, name TEXT UNIQUE NOT NULL, repos TEXT NOT NULL, init_script TEXT NOT NULL DEFAULT '')", "PRAGMA user_version=1"} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return dbError(err)
			}
		}
	} else if e := schema(ctx, tx); e != nil {
		return e
	}
	current, e := readSnapshot(ctx, tx)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(current, expected) {
		return fail("CONFIG_CONFLICT", "Configuration changed during validation; retry the command")
	}
	if e := action(tx, current); e != nil {
		return e
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}
func insertRepo(ctx context.Context, tx *sql.Tx, r Repo) *Failure {
	_, err := tx.ExecContext(ctx, "INSERT INTO repo_config (id,name,source_url,default_branch) VALUES (?,?,?,?)", r.ID, r.Name, r.URL, r.Branch)
	if err != nil {
		return dbError(err)
	}
	return nil
}
func insertWorkspace(ctx context.Context, tx *sql.Tx, w workspaceRow) *Failure {
	raw, _ := json.Marshal(w.Repos)
	_, err := tx.ExecContext(ctx, "INSERT INTO workspace_config (id,name,repos,init_script) VALUES (?,?,?,?)", w.ID, w.Name, string(raw), w.Script)
	if err != nil {
		return dbError(err)
	}
	return nil
}
