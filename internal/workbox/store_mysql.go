package workbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type mysqlStore struct{ connector *mysql.Config }

var _ configStore = (*mysqlStore)(nil)

func configuredMySQLStore() (*mysqlStore, *Failure) {
	dsn, file := os.Getenv("WORKBOX_MYSQL_DSN"), os.Getenv("WORKBOX_MYSQL_DSN_FILE")
	if file != "" {
		if dsn != "" {
			return nil, invalid("Set only one of WORKBOX_MYSQL_DSN and WORKBOX_MYSQL_DSN_FILE")
		}
		path, e := absolute(file)
		if e != nil {
			return nil, e
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, ioFailure("Could not read WORKBOX_MYSQL_DSN_FILE")
		}
		dsn = strings.TrimSpace(string(body))
	}
	if dsn == "" {
		return nil, invalid("MySQL requires WORKBOX_MYSQL_DSN or WORKBOX_MYSQL_DSN_FILE")
	}
	return newMySQLStore(dsn)
}

func newMySQLStore(dsn string) (*mysqlStore, *Failure) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || cfg.DBName == "" {
		return nil, invalid("Invalid MySQL DSN; select an existing, dedicated Workbox database")
	}
	// These settings are part of the adapter's consistency and encoding contract.
	// Never log driver/server diagnostics: they may contain connection details.
	cfg.Logger = log.New(io.Discard, "", 0)
	cfg.MultiStatements, cfg.InterpolateParams = false, false
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["autocommit"] = "1"
	if err := cfg.Apply(mysql.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
		return nil, invalid("Invalid MySQL character set configuration")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = 30 * time.Second
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	if _, err := mysql.NewConnector(cfg); err != nil {
		return nil, invalid("Invalid MySQL connection configuration")
	}
	return &mysqlStore{connector: cfg}, nil
}

func (s *mysqlStore) open() (*sql.DB, *Failure) {
	connector, err := mysql.NewConnector(s.connector)
	if err != nil {
		return nil, invalid("Invalid MySQL connection configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	return db, nil
}

func mysqlTables(ctx context.Context, conn *sql.Conn) (map[string]bool, *Failure) {
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME, ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()")
	if err != nil {
		return nil, dbError(err)
	}
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		var engine sql.NullString
		if err := rows.Scan(&name, &engine); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		if (name != "workbox_schema" && name != "repo_config" && name != "workspace_config") || !engine.Valid || !strings.EqualFold(engine.String, "InnoDB") {
			rows.Close()
			return nil, fail("SCHEMA_UNSUPPORTED", "MySQL requires a dedicated Workbox database with InnoDB tables")
		}
		tables[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, dbError(err)
	}
	return tables, nil
}

// -1 is an untouched database, 0 is resumable initialization, 1 is ready.
// Unknown tables, versions and nontransactional engines are never adopted.
func mysqlSchemaState(ctx context.Context, conn *sql.Conn) (int, *Failure) {
	tables, e := mysqlTables(ctx, conn)
	if e != nil {
		return 0, e
	}
	if len(tables) == 0 {
		return -1, nil
	}
	if !tables["workbox_schema"] {
		return 0, fail("SCHEMA_UNSUPPORTED", "Unrecognized MySQL configuration schema")
	}
	var version int
	if err := conn.QueryRowContext(ctx, "SELECT version FROM workbox_schema WHERE id=1").Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, dbError(err)
	}
	if version != 0 && version != 1 {
		return 0, fail("SCHEMA_UNSUPPORTED", "Unsupported MySQL schema version")
	}
	if version == 1 && len(tables) != 3 {
		// DDL may have finished after the first table listing but before the
		// version read. Recheck after observing the published ready marker.
		tables, e = mysqlTables(ctx, conn)
		if e != nil {
			return 0, e
		}
		if len(tables) != 3 {
			return 0, fail("SCHEMA_UNSUPPORTED", "Incomplete MySQL configuration schema")
		}
	}
	return version, nil
}

func (s *mysqlStore) schemaLock(ctx context.Context, conn *sql.Conn) (func(), *Failure) {
	name := fmt.Sprintf("workbox-schema-%x", sha256.Sum256([]byte(strings.ToLower(s.connector.DBName))))[:63]
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,5)", name).Scan(&acquired); err != nil {
		return nil, dbError(err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return nil, fail("CONFIG_CONFLICT", "MySQL schema is being initialized; retry the command")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Closing this operation's DB also closes the connection and releases
		// the named lock if cancellation or transport failure prevents this call.
		var released sql.NullInt64
		_ = conn.QueryRowContext(cleanup, "SELECT RELEASE_LOCK(?)", name).Scan(&released)
	}, nil
}

func (s *mysqlStore) initialize(ctx context.Context, conn *sql.Conn) *Failure {
	state, e := mysqlSchemaState(ctx, conn)
	if e != nil || state == 1 {
		return e
	}
	release, e := s.schemaLock(ctx, conn)
	if e != nil {
		return e
	}
	defer release()
	state, e = mysqlSchemaState(ctx, conn)
	if e != nil || state == 1 {
		return e
	}
	// MySQL DDL commits implicitly. Keep it outside the data transaction,
	// serialize initialization, and publish schema version 1 only at the end.
	statements := []string{
		"CREATE TABLE IF NOT EXISTS workbox_schema (id TINYINT NOT NULL PRIMARY KEY, version INT NOT NULL) ENGINE=InnoDB",
		"INSERT INTO workbox_schema (id,version) VALUES (1,0) ON DUPLICATE KEY UPDATE id=id",
		"CREATE TABLE IF NOT EXISTS repo_config (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, name VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE, source_url LONGTEXT NOT NULL, default_branch LONGTEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
		"CREATE TABLE IF NOT EXISTS workspace_config (id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY, name VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE, repos LONGTEXT NOT NULL, init_script LONGTEXT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin",
		"UPDATE workbox_schema SET version=1 WHERE id=1",
	}
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return dbError(err)
		}
	}
	return nil
}

func (s *mysqlStore) snapshot(ctx context.Context) (snapshot, *Failure) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, e := s.open()
	if e != nil {
		return snapshot{}, e
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return snapshot{}, dbError(err)
	}
	defer conn.Close()
	state, e := mysqlSchemaState(ctx, conn)
	if e != nil {
		return snapshot{}, e
	}
	if state == 0 {
		release, e := s.schemaLock(ctx, conn)
		if e != nil {
			return snapshot{}, e
		}
		state, e = mysqlSchemaState(ctx, conn)
		release()
		if e != nil {
			return snapshot{}, e
		}
		if state == 0 {
			// Initialization publishes version 1 before any configuration writes.
			// An interrupted version-zero schema is therefore still logically
			// empty. Leave DDL to commit so an ordinary valid command can recover.
			return emptySnapshot(), nil
		}
	}
	if state == -1 {
		return emptySnapshot(), nil
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return snapshot{}, dbError(err)
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM workbox_schema WHERE id=1").Scan(&version); err != nil {
		return snapshot{}, dbError(err)
	}
	if version != 1 {
		return snapshot{}, fail("SCHEMA_UNSUPPORTED", "Unsupported MySQL schema version")
	}
	return readSnapshot(ctx, tx)
}

func (s *mysqlStore) commit(ctx context.Context, expected, next snapshot) *Failure {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	db, e := s.open()
	if e != nil {
		return e
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return dbError(err)
	}
	defer conn.Close()
	if e := s.initialize(ctx, conn); e != nil {
		return e
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback()
	// All Workbox writers take this row lock before reading current data.
	// The first consistent read occurs after the lock, so it sees the latest
	// committed snapshot rather than the snapshot observed before Git checks.
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM workbox_schema WHERE id=1 FOR UPDATE").Scan(&version); err != nil {
		return dbError(err)
	}
	if version != 1 {
		return fail("SCHEMA_UNSUPPORTED", "Unsupported MySQL schema version")
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
