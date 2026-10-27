package workbox

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type storeFactory struct {
	name   string
	create func(*testing.T) configStore
}

func storeFactories() []storeFactory {
	return []storeFactory{
		{"sqlite", func(t *testing.T) configStore { return &sqliteStore{path: filepath.Join(t.TempDir(), "config.db")} }},
		{"mysql", mysqlTestStore},
	}
}

func storeRead(t *testing.T, s configStore) snapshot {
	t.Helper()
	value, e := s.snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return value
}

func storeCommit(t *testing.T, s configStore, old, next snapshot) {
	t.Helper()
	if e := s.commit(context.Background(), old, next); e != nil {
		t.Fatal(e)
	}
}

func storeDB(t *testing.T, s configStore) *sql.DB {
	t.Helper()
	var db *sql.DB
	var e *Failure
	switch adapter := s.(type) {
	case *sqliteStore:
		db, e = adapter.open(true)
	case *mysqlStore:
		db, e = adapter.open()
	default:
		t.Fatal("SQL fault injection requires a relational adapter")
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSQLiteResumesEmptyFile(t *testing.T) {
	s := &sqliteStore{path: filepath.Join(t.TempDir(), "config.db")}
	if err := os.WriteFile(s.path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(storeRead(t, s), emptySnapshot()) {
		t.Fatal("uninitialized SQLite file is not empty")
	}
	info, err := os.Stat(s.path)
	if err != nil || info.Size() != 0 {
		t.Fatal("read initialized empty file", err)
	}
	var out, diag bytes.Buffer
	a := &app{store: s}
	if code := a.run(context.Background(), []string{"workspace-config", "add", "example", "--format", "json"}, &out, &diag, "test"); code != 0 {
		t.Fatalf("recovery command failed: %s %s", out.String(), diag.String())
	}
	if len(storeRead(t, s).Workspaces) != 1 {
		t.Fatal("initialization recovery lost first write")
	}
}

func TestStorageContract(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			s := factory.create(t)
			empty := storeRead(t, s)
			if !sameSnapshot(empty, emptySnapshot()) {
				t.Fatal("new store is not empty")
			}
			next := empty.clone()
			next.Teams["credit"] = Team{ID: "33333333-3333-4333-8333-333333333333", Key: "credit", Name: "授信", Summary: "团队"}
			next.Repos["api"] = Repo{ID: "11111111-1111-4111-8111-111111111111", Name: "api", URL: "https://example.test/服务.git", Branch: "develop", Summary: "服务说明 🙂", TeamIDs: []string{next.Teams["credit"].ID}}
			next.Workspaces["analysis"] = workspaceRow{ID: "22222222-2222-4222-8222-222222222222", Name: "analysis", Script: "#!/bin/sh\n# 你好 🙂\nprintf ready\n", Repos: []StoredRef{{ID: next.Repos["api"].ID, Target: "repos/服务", Ref: &BaselineRef{Type: "branch", Value: "release/v2"}}}}
			storeCommit(t, s, empty, next)
			actual := storeRead(t, s)
			if !sameSnapshot(actual, next) {
				t.Fatal("stored IDs, UTF-8 or references changed")
			}
			actual.Workspaces["analysis"].Repos[0].Target = "changed-locally"
			actual.Workspaces["analysis"].Repos[0].Ref.Value = "changed-locally"
			cloned := next.clone()
			cloned.Repos["api"].TeamIDs[0] = "changed-clone"
			if next.Repos["api"].TeamIDs[0] != next.Teams["credit"].ID {
				t.Fatal("clone aliases membership")
			}
			cloned.Workspaces["analysis"].Repos[0].Ref.Value = "changed-clone"
			if next.Workspaces["analysis"].Repos[0].Ref.Value != "release/v2" {
				t.Fatal("clone aliases ref")
			}
			if !sameSnapshot(storeRead(t, s), next) {
				t.Fatal("returned snapshot aliases stored data")
			}
			updated := next.clone()
			r := updated.Repos["api"]
			r.Branch = "release"
			updated.Repos["api"] = r
			storeCommit(t, s, next, updated)
			if e := s.commit(context.Background(), next, empty); e == nil || e.Code != "CONFIG_CONFLICT" {
				t.Fatal("stale snapshot was accepted", e)
			}
			if !sameSnapshot(storeRead(t, s), updated) {
				t.Fatal("stale commit overwrote current data")
			}
			storeCommit(t, s, updated, empty)
			if !sameSnapshot(storeRead(t, s), empty) {
				t.Fatal("removal did not persist")
			}
		})
	}
}

func TestStorageConcurrentFirstWrite(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			s := factory.create(t)
			old := storeRead(t, s)
			start := make(chan struct{})
			results := make(chan *Failure, 6)
			var wg sync.WaitGroup
			for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
				id, e := uuid()
				if e != nil {
					t.Fatal(e)
				}
				next := old.clone()
				next.Workspaces[name] = workspaceRow{ID: id, Name: name, Repos: []StoredRef{}}
				wg.Add(1)
				go func() { defer wg.Done(); <-start; results <- s.commit(context.Background(), old, next) }()
			}
			close(start)
			wg.Wait()
			close(results)
			count := 0
			for e := range results {
				if e == nil {
					count++
				} else if e.Code != "CONFIG_CONFLICT" {
					t.Fatal(e)
				}
			}
			if count != 1 || len(storeRead(t, s).Workspaces) != 1 {
				t.Fatalf("concurrent first commits succeeded %d times", count)
			}
		})
	}
}

func TestStorageWriteFailureRollsBackAllChanges(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			s := factory.create(t)
			old := emptySnapshot()
			old.Repos["api"] = Repo{ID: "11111111-1111-4111-8111-111111111111", Name: "api", URL: "https://example.test/api.git", Branch: "develop"}
			storeCommit(t, s, emptySnapshot(), old)
			db := storeDB(t, s)
			statement := "CREATE TRIGGER fail_workspace BEFORE INSERT ON workspace_config BEGIN SELECT RAISE(ABORT,'injected'); END"
			if factory.name == "mysql" {
				statement = "CREATE TRIGGER fail_workspace BEFORE INSERT ON workspace_config FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected'"
			}
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			next := old.clone()
			r := next.Repos["api"]
			r.Branch = "release"
			next.Repos["api"] = r
			next.Workspaces["new"] = workspaceRow{ID: "22222222-2222-4222-8222-222222222222", Name: "new", Repos: []StoredRef{{ID: r.ID, Target: "api"}}}
			if e := s.commit(context.Background(), old, next); e == nil {
				t.Fatal("injected write succeeded")
			}
			if !sameSnapshot(storeRead(t, s), old) {
				t.Fatal("failed transaction left partial changes")
			}
		})
	}
}

func TestBackendSelectionAndCredentialRedaction(t *testing.T) {
	home := initHome(t)
	t.Setenv("WORKBOX_DB", "sqlite")
	t.Setenv("WORKBOX_MYSQL_DSN", "")
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", "")
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	if s, e := configuredStore(); e != nil {
		t.Fatal(e)
	} else if _, ok := s.(*sqliteStore); !ok {
		t.Fatal("SQLite selection failed")
	}
	t.Setenv("WORKBOX_DB", "mysql")
	for _, dsn := range []string{"", "private-password-is-not-a-dsn", "user:private-password@tcp(127.0.0.1:1)/"} {
		t.Setenv("WORKBOX_MYSQL_DSN", dsn)
		if _, e := configuredStore(); e == nil || e.Code != "CONFIG_INVALID" || strings.Contains(e.Error(), "private-password") {
			t.Fatal(e)
		}
	}
	dsn := "user:private-password@tcp(127.0.0.1:1)/workbox"
	file := filepath.Join(home, "mysql-dsn")
	if err := os.WriteFile(file, []byte(dsn+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBOX_MYSQL_DSN", "")
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", file)
	s, e := configuredStore()
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := s.(*mysqlStore); !ok {
		t.Fatal("MySQL file selection failed")
	}
	if _, e := s.snapshot(context.Background()); e == nil || strings.Contains(e.Error(), "private-password") {
		t.Fatal("connection error leaked credentials or unexpectedly succeeded", e)
	}
	t.Setenv("WORKBOX_MYSQL_DSN", dsn)
	if _, e := configuredStore(); e == nil || e.Code != "CONFIG_INVALID" {
		t.Fatal("ambiguous credentials accepted", e)
	}
	t.Setenv("WORKBOX_DB", "unknown")
	if _, e := configuredStore(); e == nil || e.Code != "CONFIG_INVALID" {
		t.Fatal(e)
	}
}

func TestLocalCommandsDoNotResolveBackend(t *testing.T) {
	a, home := skillApp(t)
	t.Setenv("WORKBOX_DB", "unknown")
	for _, args := range [][]string{{"--help"}, {"--version"}, {"workspace", "status", "--path", home}, {"skill", "install", "--path", filepath.Join(home, "skills")}} {
		skillCall(t, a, 0, args...)
	}
	if a.store != nil {
		t.Fatal("local command selected a configuration backend")
	}
}

// A non-SQL test adapter also exercises the business interface. It is not a
// selectable runtime backend and knows nothing about drivers or SQL dialects.
type memoryConfigStore struct{ value snapshot }

func (s *memoryConfigStore) snapshot(context.Context) (snapshot, *Failure) {
	return s.value.clone(), nil
}
func (s *memoryConfigStore) commit(_ context.Context, old, next snapshot) *Failure {
	if !sameSnapshot(s.value, old) {
		return fail("CONFIG_CONFLICT", "changed")
	}
	s.value = next.clone()
	return nil
}
func TestBusinessUsesStorageInterface(t *testing.T) {
	s := &memoryConfigStore{value: emptySnapshot()}
	a := &app{store: s}
	var out, diag bytes.Buffer
	if code := a.run(context.Background(), []string{"workspace-config", "add", "example", "--format", "json"}, &out, &diag, "test"); code != 0 {
		t.Fatalf("%s %s", out.String(), diag.String())
	}
	if len(s.value.Workspaces) != 1 || s.value.Workspaces["example"].ID == "" {
		t.Fatal("business operation did not persist through interface")
	}
}
