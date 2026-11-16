package workbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type instanceRegistry struct{ path string }

func localInstances() (instanceRegistry, *Failure) {
	root, e := userStateDir()
	return instanceRegistry{filepath.Join(root, "instances.db")}, e
}

func registryDB(path, mode string) (*sql.DB, error) {
	query := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(5000)"}}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}
	return sql.Open("sqlite", uri.String())
}

// An absent registry is an empty catalog. Other open errors must reach the caller.
func (r instanceRegistry) existing(mode string) (*sql.DB, error) {
	info, err := os.Lstat(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular database file", r.path)
	}
	return registryDB(r.path, mode)
}

func (r instanceRegistry) failure(err error) *Failure {
	return ioFailure(fmt.Sprintf("Instance registry %s: %v", r.path, err))
}

func (r instanceRegistry) checkAvailable(ctx context.Context, path string) *Failure {
	db, err := r.existing("ro")
	if err != nil {
		return r.failure(err)
	}
	if db == nil {
		return nil
	}
	defer db.Close()
	var occupied bool
	err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT path,config_name,created_at FROM instances WHERE path=?)", path).Scan(&occupied)
	if err != nil {
		return r.failure(err)
	}
	if occupied {
		return fail("TARGET_EXISTS", "Instance path is registered; use workbox forget --path before reusing "+path)
	}
	return nil
}

// Publish a complete schema exclusively so readers never see an empty database
// during concurrent first use. Existing registries are never reinitialized.
func (r instanceRegistry) initialize(ctx context.Context) error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".instances-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Close(); err != nil {
		return err
	}
	db, err := registryDB(file.Name(), "rw")
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "CREATE TABLE instances (path TEXT PRIMARY KEY NOT NULL, config_name TEXT NOT NULL, created_at TEXT NOT NULL)")
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(file.Name(), r.path); !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

func (r instanceRegistry) register(ctx context.Context, path, name string) error {
	db, err := r.existing("rw")
	if err != nil {
		return err
	}
	if db == nil {
		if err := r.initialize(ctx); err != nil {
			return err
		}
		db, err = r.existing("rw")
		if err != nil {
			return err
		}
		if db == nil {
			return fmt.Errorf("instance registry disappeared: %s", r.path)
		}
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "INSERT INTO instances(path,config_name,created_at) VALUES(?,?,?)", path, name, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type localInstance struct {
	Path       string `json:"path"`
	ConfigName string `json:"config_name"`
	CreatedAt  string `json:"created_at"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

func (r instanceRegistry) list(ctx context.Context) (any, *Failure) {
	items := []localInstance{}
	db, err := r.existing("ro")
	if err != nil {
		return nil, r.failure(err)
	}
	if db != nil {
		defer db.Close()
		rows, err := db.QueryContext(ctx, "SELECT path,config_name,created_at FROM instances ORDER BY path")
		if err != nil {
			return nil, r.failure(err)
		}
		defer rows.Close()
		for rows.Next() {
			var item localInstance
			if err := rows.Scan(&item.Path, &item.ConfigName, &item.CreatedAt); err != nil {
				return nil, r.failure(err)
			}
			info, err := os.Stat(item.Path)
			switch {
			case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
				item.Status = "missing"
			case err != nil:
				item.Status = "unknown"
				item.Reason = err.Error()
			case info.IsDir():
				item.Status = "present"
			default:
				item.Status = "not_directory"
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return nil, r.failure(err)
		}
	}
	return map[string]any{"instances": items}, nil
}

func (r instanceRegistry) forget(ctx context.Context, raw string) (any, *Failure) {
	path, e := absolute(raw)
	if e != nil {
		return nil, e
	}
	db, err := r.existing("rw")
	if err != nil {
		return nil, r.failure(err)
	}
	unknown := fail("PATH_INVALID", "No instance registered for "+path)
	if db == nil {
		return nil, unknown
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, r.failure(err)
	}
	defer tx.Rollback()
	// Match the recorded spelling first, even if the path now points elsewhere.
	remove := func(candidate string) error {
		return tx.QueryRowContext(ctx, "DELETE FROM instances WHERE path=? RETURNING path", candidate).Scan(&path)
	}
	err = remove(path)
	if errors.Is(err, sql.ErrNoRows) {
		alias, resolveErr := filepath.EvalSymlinks(path)
		if errors.Is(resolveErr, os.ErrNotExist) {
			// A missing instance can still have a reachable parent alias.
			var parent string
			parent, resolveErr = filepath.EvalSymlinks(filepath.Dir(path))
			alias = filepath.Join(parent, filepath.Base(path))
		}
		if errors.Is(resolveErr, os.ErrNotExist) || alias == path {
			return nil, unknown
		}
		if resolveErr != nil {
			return nil, ioFailure(resolveErr.Error())
		}
		err = remove(alias)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, unknown
	}
	if err != nil {
		return nil, r.failure(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, r.failure(err)
	}
	return map[string]any{"forgotten_path": path}, nil
}
