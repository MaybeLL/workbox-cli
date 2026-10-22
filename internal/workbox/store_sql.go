package workbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/go-sql-driver/mysql"
)

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

// Persist only changed rows. Both relational adapters call this inside their
// own transaction after acquiring their write lock and comparing snapshots.
func writeSnapshot(ctx context.Context, tx *sql.Tx, current, next snapshot) *Failure {
	for _, name := range names(current.Workspaces) {
		old := current.Workspaces[name]
		if value, ok := next.Workspaces[name]; !ok || !reflect.DeepEqual(old, value) {
			if _, err := tx.ExecContext(ctx, "DELETE FROM workspace_config WHERE id=?", old.ID); err != nil {
				return dbError(err)
			}
		}
	}
	for _, name := range names(current.Repos) {
		old := current.Repos[name]
		if value, ok := next.Repos[name]; !ok || old != value {
			if _, err := tx.ExecContext(ctx, "DELETE FROM repo_config WHERE id=?", old.ID); err != nil {
				return dbError(err)
			}
		}
	}
	for _, name := range names(next.Repos) {
		value := next.Repos[name]
		if old, ok := current.Repos[name]; !ok || old != value {
			if e := insertRepo(ctx, tx, value); e != nil {
				return e
			}
		}
	}
	for _, name := range names(next.Workspaces) {
		value := next.Workspaces[name]
		if old, ok := current.Workspaces[name]; !ok || !reflect.DeepEqual(old, value) {
			if e := insertWorkspace(ctx, tx, value); e != nil {
				return e
			}
		}
	}
	return nil
}

func dbError(err error) *Failure {
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && (mysqlError.Number == 1062 || mysqlError.Number == 1205 || mysqlError.Number == 1213) {
		return fail("CONFIG_CONFLICT", "Configuration database is busy or changed concurrently")
	}
	if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "SQLITE_LOCKED") {
		return fail("CONFIG_CONFLICT", "Configuration database is busy")
	}
	return ioFailure("Could not read or write configuration database")
}
