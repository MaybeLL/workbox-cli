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

func readSnapshot(ctx context.Context, tx *sql.Tx, version int) (snapshot, *Failure) {
	s := emptySnapshot()
	columns := "id,name,source_url,default_branch"
	if version >= 2 {
		columns += ",summary"
	}
	if version >= 3 {
		columns += ",team_ids"
		rows, err := tx.QueryContext(ctx, "SELECT id,team_key,name,summary FROM team_config ORDER BY team_key")
		if err != nil {
			return s, dbError(err)
		}
		for rows.Next() {
			var t Team
			if err := rows.Scan(&t.ID, &t.Key, &t.Name, &t.Summary); err != nil {
				rows.Close()
				return s, dbError(err)
			}
			s.Teams[t.Key] = t
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s, dbError(err)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM repo_config ORDER BY name")
	if err != nil {
		return s, dbError(err)
	}
	for rows.Next() {
		var r Repo
		var teamIDs string
		dest := []any{&r.ID, &r.Name, &r.URL, &r.Branch}
		if version >= 2 {
			dest = append(dest, &r.Summary)
		}
		if version >= 3 {
			dest = append(dest, &teamIDs)
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return s, dbError(err)
		}
		if version >= 3 {
			if json.Unmarshal([]byte(teamIDs), &r.TeamIDs) != nil {
				rows.Close()
				return s, invalid("Invalid stored team IDs")
			}
			if len(r.TeamIDs) == 0 {
				r.TeamIDs = nil
			}
			if _, e := s.publicRepo(r); e != nil {
				rows.Close()
				return s, e
			}
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
	ids, _ := json.Marshal(append([]string{}, r.TeamIDs...))
	_, err := tx.ExecContext(ctx, "INSERT INTO repo_config (id,name,source_url,default_branch,summary,team_ids) VALUES (?,?,?,?,?,?)", r.ID, r.Name, r.URL, r.Branch, r.Summary, string(ids))
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
	for key, old := range current.Teams {
		if value, ok := next.Teams[key]; !ok || old != value {
			if _, err := tx.ExecContext(ctx, "DELETE FROM team_config WHERE id=?", old.ID); err != nil {
				return dbError(err)
			}
		}
	}
	for key, value := range next.Teams {
		if old, ok := current.Teams[key]; !ok || old != value {
			if _, err := tx.ExecContext(ctx, "INSERT INTO team_config (id,team_key,name,summary) VALUES (?,?,?,?)", value.ID, value.Key, value.Name, value.Summary); err != nil {
				return dbError(err)
			}
		}
	}

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
		if value, ok := next.Repos[name]; !ok || !reflect.DeepEqual(old, value) {
			if _, err := tx.ExecContext(ctx, "DELETE FROM repo_config WHERE id=?", old.ID); err != nil {
				return dbError(err)
			}
		}
	}
	for _, name := range names(next.Repos) {
		value := next.Repos[name]
		if old, ok := current.Repos[name]; !ok || !reflect.DeepEqual(old, value) {
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
