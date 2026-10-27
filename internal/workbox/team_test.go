package workbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamLifecycleAndMembership(t *testing.T) {
	f := setup(t)
	first := object(object(f.call(0, "team-config", "add", "credit", "--name", "授信团队", "--summary", "授信业务")["data"])["team_config"])
	f.call(0, "team-config", "add", "fraud", "--name", "反欺诈")
	f.repo("api")
	f.call(0, "repo-config", "add-team", "api", "--team", "credit")
	f.call(0, "repo-config", "add-team", "api", "--team", "credit")
	f.call(0, "repo-config", "add-team", "api", "--team", "fraud")
	r := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if len(array(r["teams"])) != 2 {
		t.Fatal(r)
	}
	stored := storeRead(t, f.sqlite)
	if len(stored.Repos["api"].TeamIDs) != 2 || stored.Repos["api"].TeamIDs[0] != first["id"] {
		t.Fatal(stored)
	}
	f.call(2, "team-config", "remove", "credit")
	f.call(2, "team-config", "add", "credit", "--name", "Duplicate")
	f.call(2, "repo-config", "add-team", "api", "--team", "missing")
	f.call(2, "repo-config", "update", "api", "--teams", "credit,credit")
	f.call(2, "team-config", "update", "credit", "--key", "new")
	f.call(0, "team-config", "update", "credit", "--name", "授信新名称", "--summary", "")
	team := object(object(f.call(0, "team-config", "show", "credit")["data"])["team_config"])
	if team["id"] != first["id"] || team["name"] != "授信新名称" {
		t.Fatal(team)
	}
	filtered := object(f.call(0, "repo-config", "list", "--team", "credit")["data"])
	if len(array(filtered["repo_configs"])) != 1 {
		t.Fatal(filtered)
	}
	f.call(2, "repo-config", "list", "--team", "unknown")
	f.workspace([]Ref{{Repo: "api", Target: "api"}}, "")
	plan := object(f.call(0, "workspace", "plan", "--config", "analysis", "--path", filepath.Join(f.root, "work"))["data"])
	teams := array(object(array(plan["repos"])[0])["teams"])
	if object(teams[0])["name"] != "授信新名称" {
		t.Fatal(teams)
	}
	f.call(0, "repo-config", "remove-team", "api", "--team", "credit")
	f.call(0, "team-config", "remove", "credit")
	f.call(0, "repo-config", "update", "api", "--teams", "")
	f.call(0, "team-config", "remove", "fraud")
}

func TestTeamPortableRoundTrip(t *testing.T) {
	f := setup(t)
	f.call(0, "team-config", "add", "credit", "--name", "授信")
	f.call(0, "repo-config", "add", "api", "--url", "https://example.test/api.git", "--default-branch", "develop", "--teams", "credit")
	original := storeRead(t, f.sqlite)
	file := filepath.Join(f.root, "export.json")
	f.call(0, "config", "export", "--output", file)
	raw, _ := os.ReadFile(file)
	if strings.Contains(string(raw), original.Teams["credit"].ID) {
		t.Fatal("export contains local team ID")
	}
	f.app.store = &sqliteStore{path: filepath.Join(f.root, "imported.db")}
	f.call(0, "config", "import", "--input", file)
	f.call(0, "config", "import", "--input", file)
	imported := storeRead(t, f.app.store)
	if imported.Teams["credit"].ID == original.Teams["credit"].ID || imported.Repos["api"].TeamIDs[0] != imported.Teams["credit"].ID {
		t.Fatal(imported)
	}
	f.call(0, "team-config", "update", "credit", "--name", "Local edit")
	f.call(2, "config", "import", "--input", file)
	var pkg map[string]any
	_ = json.Unmarshal(raw, &pkg)
	delete(pkg, "team_configs")
	bad, _ := json.Marshal(pkg)
	f.call(2, "config", "import", "--input", f.file("dangling.json", string(bad)))
}

func TestTeamMigration(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			f := setup(t)
			s := factory.create(t)
			f.app.store = s
			f.repo("api")
			db := storeDB(t, s)
			for _, q := range []string{"DROP TABLE team_config", "ALTER TABLE repo_config DROP COLUMN team_ids"} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			versionQuery := "PRAGMA user_version"
			setVersion := "PRAGMA user_version=2"
			if factory.name == "mysql" {
				versionQuery = "SELECT version FROM workbox_schema WHERE id=1"
				setVersion = "UPDATE workbox_schema SET version=2 WHERE id=1"
			}
			if _, err := db.Exec(setVersion); err != nil {
				t.Fatal(err)
			}
			before := storeRead(t, s)
			var version int
			if err := db.QueryRow(versionQuery).Scan(&version); err != nil || version != 2 {
				t.Fatal("read mutated schema", version, err)
			}
			f.call(0, "team-config", "add", "credit", "--name", "授信")
			f.call(0, "repo-config", "add-team", "api", "--team", "credit")
			after := storeRead(t, s)
			if after.Repos["api"].ID != before.Repos["api"].ID || len(after.Repos["api"].TeamIDs) != 1 {
				t.Fatal(after)
			}
			if err := db.QueryRow(versionQuery).Scan(&version); err != nil || version != 3 {
				t.Fatal(version, err)
			}
		})
	}
}

func TestTeamImportValidationAndAtomicity(t *testing.T) {
	f := setup(t)
	valid := `{"schema_version":1,"team_configs":[{"key":"credit","name":"授信"}],"repo_configs":[],"workspace_configs":[]}`
	for _, raw := range []string{
		strings.Replace(valid, `[{"key":"credit","name":"授信"}]`, `null`, 1),
		strings.Replace(valid, `"key":"credit"`, `"key":"Bad Key"`, 1),
		strings.Replace(valid, `"name":"授信"`, `"name":" "`, 1),
		strings.Replace(valid, `"name":"授信"`, `"name":"授信","id":"foreign-id"`, 1),
		strings.Replace(valid, `[{"key":"credit","name":"授信"}]`, `[{"key":"credit","name":"授信"},{"key":"credit","name":"other"}]`, 1),
	} {
		f.call(2, "config", "import", "--input", f.file("bad.json", raw))
	}
	if len(storeRead(t, f.sqlite).Teams) != 0 {
		t.Fatal("invalid import wrote teams")
	}
	f.call(0, "config", "import", "--input", f.file("teams.json", valid))
	f.call(0, "config", "import", "--input", f.file("teams.json", valid))
	f.call(2, "team-config", "update", "credit")
	f.call(2, "team-config", "add", "bad key", "--name", "bad")
}

func TestMembershipConcurrentWriteRejected(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.call(0, "team-config", "add", "credit", "--name", "Credit")
	expected := storeRead(t, f.sqlite)
	f.call(0, "repo-config", "add-team", "api", "--team", "credit")
	if e := f.sqlite.commit(context.Background(), expected, expected); e == nil || e.Code != "CONFIG_CONFLICT" {
		t.Fatal(e)
	}
	if len(storeRead(t, f.sqlite).Repos["api"].TeamIDs) != 1 {
		t.Fatal("stale write erased membership")
	}
}
