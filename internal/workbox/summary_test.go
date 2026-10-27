package workbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepoSummaryLifecycle(t *testing.T) {
	f := setup(t)
	summary := "策略服务：执行与编排\nNavigation only 🙂"
	r := object(object(f.call(0, "repo-config", "add", "api", "--url", "https://example.test/api.git", "--default-branch", "develop", "--summary", summary)["data"])["repo_config"])
	if r["summary"] != summary {
		t.Fatal(r)
	}
	f.workspace([]Ref{{Repo: "api", Target: "api"}}, "")
	plan := object(f.call(0, "workspace", "plan", "--config", "analysis", "--path", filepath.Join(f.root, "work"))["data"])
	if object(array(plan["repos"])[0])["summary"] != summary {
		t.Fatal(plan)
	}
	f.call(0, "repo-config", "update", "api", "--default-branch", "develop")
	r = object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if r["summary"] != summary {
		t.Fatal("unrelated update lost summary")
	}
	exported := filepath.Join(f.root, "export.json")
	f.call(0, "config", "export", "--output", exported)
	f.call(0, "config", "import", "--input", exported)
	f.app.store = &sqliteStore{path: filepath.Join(f.root, "other.db")}
	f.call(0, "config", "import", "--input", exported)
	r = object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if r["summary"] != summary {
		t.Fatal("transfer lost summary")
	}
	f.call(0, "repo-config", "update", "api", "--summary", "")
	r = object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if r["summary"] != "" {
		t.Fatal("did not clear summary")
	}
	if errorCode(f.call(2, "config", "import", "--input", exported)) != "CONFIG_CONFLICT" {
		t.Fatal("conflicting summary overwritten")
	}
	// Version-1 portable catalogs without the optional field still import.
	raw, _ := os.ReadFile(exported)
	var pkg map[string]any
	_ = json.Unmarshal(raw, &pkg)
	delete(object(array(pkg["repo_configs"])[0]), "summary")
	raw, _ = json.Marshal(pkg)
	f.call(0, "config", "import", "--input", f.file("legacy.json", string(raw)))
	for _, value := range []any{nil, 12, true} {
		object(array(pkg["repo_configs"])[0])["summary"] = value
		raw, _ = json.Marshal(pkg)
		if errorCode(f.call(2, "config", "import", "--input", f.file("invalid.json", string(raw)))) != "CONFIG_INVALID" {
			t.Fatal("invalid summary accepted")
		}
	}
}

func TestSQLiteSummaryMigrationReadOnly(t *testing.T) {
	f := setup(t)
	f.sql("CREATE TABLE repo_config (id TEXT PRIMARY KEY,name TEXT UNIQUE,source_url TEXT,default_branch TEXT)")
	f.sql("CREATE TABLE workspace_config (id TEXT PRIMARY KEY,name TEXT UNIQUE,repos TEXT,init_script TEXT)")
	f.sql("PRAGMA user_version=1")
	f.sql("INSERT INTO repo_config VALUES ('old-id','api','https://example.test/api.git','develop')")
	r := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if r["summary"] != "" || r["id"] != "old-id" {
		t.Fatal(r)
	}
	db := storeDB(t, f.sqlite)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("read migrated schema", version, err)
	}
	f.call(0, "repo-config", "update", "api", "--summary", "new")
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal("write did not migrate", version, err)
	}
	r = object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if r["summary"] != "new" || r["id"] != "old-id" {
		t.Fatal(r)
	}
}

func TestMySQLSummaryMigration(t *testing.T) {
	f := setup(t)
	s := mysqlTestStore(t)
	f.app.store = s
	f.repo("api")
	db := storeDB(t, s)
	if _, err := db.Exec("ALTER TABLE repo_config DROP COLUMN summary"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE workbox_schema SET version=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	before := storeRead(t, s)
	var version int
	if err := db.QueryRow("SELECT version FROM workbox_schema WHERE id=1").Scan(&version); err != nil || version != 1 {
		t.Fatal(version, err)
	}
	// Simulate interrupted DDL before version publication.
	if _, err := db.Exec("ALTER TABLE repo_config ADD COLUMN summary LONGTEXT NOT NULL DEFAULT ('')"); err != nil {
		t.Fatal(err)
	}
	f.call(0, "repo-config", "update", "api", "--summary", "服务说明")
	after := storeRead(t, s)
	if after.Repos["api"].ID != before.Repos["api"].ID || after.Repos["api"].Summary != "服务说明" {
		t.Fatal(after)
	}
}
