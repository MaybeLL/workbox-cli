package workbox

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type sqliteStore struct{ path string }

var _ configStore = (*sqliteStore)(nil)

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
func (s *sqliteStore) open(write bool) (*sql.DB, *Failure) {
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
func sqliteSchema(ctx context.Context, tx *sql.Tx) (bool, *Failure) {
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return false, dbError(err)
	}
	if version == 1 {
		return true, nil
	}
	if version == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
			return false, dbError(err)
		}
		if count == 0 {
			return false, nil
		}
	}
	return false, fail("SCHEMA_UNSUPPORTED", "Unsupported database schema version")
}
func (s *sqliteStore) snapshot(ctx context.Context) (snapshot, *Failure) {
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
	ready, e := sqliteSchema(ctx, tx)
	if e != nil {
		return snapshot{}, e
	}
	if !ready {
		return emptySnapshot(), nil
	}
	return readSnapshot(ctx, tx)
}
func (s *sqliteStore) commit(ctx context.Context, expected, next snapshot) *Failure {
	_, err := pathExists(s.path)
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
	ready, e := sqliteSchema(ctx, tx)
	if e != nil {
		return e
	}
	// Another first writer may have created the empty file before acquiring
	// its transaction. An empty, version-zero SQLite file is still uninitialized.
	if !ready {
		for _, ddl := range []string{"CREATE TABLE repo_config (id TEXT PRIMARY KEY NOT NULL, name TEXT UNIQUE NOT NULL, source_url TEXT NOT NULL, default_branch TEXT NOT NULL)", "CREATE TABLE workspace_config (id TEXT PRIMARY KEY NOT NULL, name TEXT UNIQUE NOT NULL, repos TEXT NOT NULL, init_script TEXT NOT NULL DEFAULT '')", "PRAGMA user_version=1"} {
			if _, err := tx.ExecContext(ctx, ddl); err != nil {
				return dbError(err)
			}
		}
	}
	current, e := readSnapshot(ctx, tx)
	if e != nil {
		return e
	}
	if !sameSnapshot(current, expected) {
		return fail("CONFIG_CONFLICT", "Configuration changed during validation; retry the command")
	}
	if e := writeSnapshot(ctx, tx, current, next); e != nil {
		return e
	}
	if err := tx.Commit(); err != nil {
		return dbError(err)
	}
	return nil
}
