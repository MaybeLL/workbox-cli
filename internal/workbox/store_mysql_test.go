package workbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Only the explicit integration-test DSN is used. Each test creates and owns a
// random database; deployment WORKBOX_MYSQL_DSN is never used for test cleanup.
func mysqlTestStore(t *testing.T) configStore {
	t.Helper()
	dsn := os.Getenv("WORKBOX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set WORKBOX_TEST_MYSQL_DSN to a disposable test server")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid integration test DSN")
	}
	cfg.DBName = ""
	cfg.Logger = log.New(io.Discard, "", 0)
	cfg.Timeout = 5 * time.Second
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid integration connection")
	}
	admin := sql.OpenDB(connector)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatal("integration MySQL server is unavailable")
	}
	id, e := uuid()
	if e != nil {
		admin.Close()
		t.Fatal(e)
	}
	name := "workbox_test_" + strings.ReplaceAll(id, "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		admin.Close()
		t.Fatal("could not create isolated test database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("could not remove owned integration test database")
		}
		admin.Close()
	})
	cfg.DBName = name
	// Explicitly conflicting session parameters must not disable adapter commits.
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["autocommit"] = "0"
	s, e := newMySQLStore(cfg.FormatDSN())
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestMySQLReadDoesNotInitialize(t *testing.T) {
	s := mysqlTestStore(t)
	if !sameSnapshot(storeRead(t, s), emptySnapshot()) {
		t.Fatal("new MySQL database is not empty")
	}
	db := storeDB(t, s)
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("read initialized schema")
	}
}

func TestMySQLResumesInterruptedInitialization(t *testing.T) {
	s := mysqlTestStore(t)
	db := storeDB(t, s)
	for _, q := range []string{"CREATE TABLE workbox_schema (id TINYINT PRIMARY KEY, version INT NOT NULL) ENGINE=InnoDB", "INSERT INTO workbox_schema VALUES (1,0)"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if !sameSnapshot(storeRead(t, s), emptySnapshot()) {
		t.Fatal("interrupted initialization is not logically empty")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE()").Scan(&count); err != nil || count != 1 {
		t.Fatal("read resumed DDL", err, count)
	}
	// Exercise the complete command path: it reads before committing.
	var out, diag strings.Builder
	a := &app{store: s}
	if code := a.run(context.Background(), []string{"workspace-config", "add", "example", "--format", "json"}, &out, &diag, "test"); code != 0 {
		t.Fatalf("recovery command failed: %s %s", out.String(), diag.String())
	}
	if len(storeRead(t, s).Workspaces) != 1 {
		t.Fatal("initialization recovery lost first write")
	}
}

func TestMySQLRejectsUnknownSchema(t *testing.T) {
	for _, kind := range []string{"foreign_table", "version", "engine"} {
		t.Run(kind, func(t *testing.T) {
			s := mysqlTestStore(t)
			if kind != "foreign_table" {
				storeCommit(t, s, emptySnapshot(), emptySnapshot())
			}
			db := storeDB(t, s)
			q := "CREATE TABLE unrelated (id INT) ENGINE=InnoDB"
			if kind == "version" {
				q = "UPDATE workbox_schema SET version=99 WHERE id=1"
			}
			if kind == "engine" {
				q = "ALTER TABLE workspace_config ENGINE=MyISAM"
			}
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
			if _, e := s.snapshot(context.Background()); e == nil || e.Code != "SCHEMA_UNSUPPORTED" {
				t.Fatal(e)
			}
			if e := s.commit(context.Background(), emptySnapshot(), emptySnapshot()); e == nil || e.Code != "SCHEMA_UNSUPPORTED" {
				t.Fatal(e)
			}
		})
	}
}

func TestMySQLBusinessAndCrossBackendTransfer(t *testing.T) {
	f := setup(t)
	s := mysqlTestStore(t)
	f.app.store = s
	f.repo("api")
	script := "#!/bin/sh\n# " + strings.Repeat("配置🙂", 12000) + "\n"
	f.workspace([]Ref{{Repo: "api", Target: "repos/服务"}}, script)
	if errorCode(f.call(2, "repo-config", "remove", "api")) != "CONFIG_CONFLICT" {
		t.Fatal("reference protection failed")
	}
	root := filepath.Join(f.root, "mysql-workspace")
	f.call(0, "workspace", "plan", "--config", "analysis", "--path", root)
	assertAbsent(t, root)
	f.call(0, "workspace", "create", "--config", "analysis", "--path", root)
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	status := object(f.call(0, "workspace", "status", "--path", root)["data"])
	if len(array(status["repos"])) != 1 || object(array(status["repos"])[0])["clean"] != true {
		t.Fatal(status)
	}
	original := storeRead(t, s)
	first := filepath.Join(f.root, "from-mysql.json")
	f.call(0, "config", "export", "--output", first)
	f.app.store = &sqliteStore{path: filepath.Join(f.root, "imported-sqlite", "config.db")}
	f.call(0, "config", "import", "--input", first)
	local := storeRead(t, f.app.store)
	if local.Repos["api"].ID == original.Repos["api"].ID || local.Workspaces["analysis"].Script != script {
		t.Fatal("cross-backend transfer lost script or reused source IDs")
	}
	second := filepath.Join(f.root, "from-sqlite.json")
	f.call(0, "config", "export", "--output", second)
	f.app.store = mysqlTestStore(t)
	f.call(0, "config", "import", "--input", second)
	result := object(f.call(0, "config", "import", "--input", second)["data"])
	if len(array(object(result["added"])["repo_configs"])) != 0 || len(array(object(result["unchanged"])["workspace_configs"])) != 1 {
		t.Fatal(result)
	}
	final := storeRead(t, f.app.store)
	a, e := original.portable()
	if e != nil {
		t.Fatal(e)
	}
	b, e := final.portable()
	if e != nil {
		t.Fatal(e)
	}
	if !jsonEqual(a, b) {
		t.Fatal("SQLite/MySQL round-trip changed configuration")
	}
	assertAbsent(t, f.sqlite.path)
}

func TestMySQLCLIUsesDSNFile(t *testing.T) {
	f := setup(t)
	s := mysqlTestStore(t).(*mysqlStore)
	file := f.file("private-mysql-dsn", s.connector.FormatDSN()+"\n")
	t.Setenv("WORKBOX_DB", "mysql")
	t.Setenv("WORKBOX_MYSQL_DSN", "")
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", file)
	initCmd := exec.Command(testBinary, "init", "--backend", "mysql", "--skip-skill", "--format", "json")
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %s", out)
	}
	cmd := exec.Command(testBinary, "workspace-config", "add", "from-cli", "--format", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("CLI failed: %s", out)
	}
	var response map[string]any
	if json.Unmarshal(out, &response) != nil || response["ok"] != true || len(storeRead(t, s).Workspaces) != 1 {
		t.Fatalf("invalid CLI result: %s", out)
	}
	assertAbsent(t, f.sqlite.path)
}
