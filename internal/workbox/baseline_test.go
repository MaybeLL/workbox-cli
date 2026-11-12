package workbox

import (
	"encoding/json"
	"os"
	"path/filepath"

	"testing"
)

func TestWorkspaceBaselineOverrides(t *testing.T) {
	for _, kind := range []string{"branch", "commit"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.repo("api")
			remote := filepath.Join(f.remotes, "api.git")
			sha := f.git(remote, "rev-parse", "develop~1")
			f.git(remote, "branch", "release", sha)
			value := sha
			if kind == "branch" {
				value = "release"
			}
			f.workspace([]Ref{{Repo: "api", Target: "repos/api", Ref: &BaselineRef{Type: kind, Value: value}}}, "")
			// Reopening the SQL store and exporting preserve the override.
			shown := object(object(f.call(0, "workbox-config", "show", "analysis")["data"])["workspace_config"])
			if object(object(array(shown["repos"])[0])["ref"])["value"] != value {
				t.Fatal(shown)
			}
			export := filepath.Join(f.root, "export.json")
			f.call(0, "config", "export", "--output", export)
			f.call(0, "config", "import", "--input", export)
			f.call(0, "workbox-config", "remove", "analysis")
			f.call(0, "config", "import", "--input", export)
			target := filepath.Join(f.root, "workspace")
			plan := object(f.call(0, "plan", "--config", "analysis", "--path", target)["data"])
			if object(object(array(plan["repos"])[0])["ref"])["value"] != value {
				t.Fatal(plan)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("plan created target")
			}
			result := object(f.call(0, "create", "--config", "analysis", "--path", target)["data"])
			repo := object(array(result["repos"])[0])
			if object(repo["ref"])["value"] != value || repo["default_branch"] != "develop" {
				t.Fatal(repo)
			}
			assertAbsent(t, filepath.Join(target, "repos/api"))
			original := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
			if original["default_branch"] != "develop" {
				t.Fatal(original)
			}
			// Omitting ref on update clears the override and restores inheritance.
			raw, _ := json.Marshal([]Ref{{Repo: "api", Target: "repos/api"}})
			f.call(0, "workbox-config", "update", "analysis", "--repos-file", f.file("clear.json", string(raw)))
			second := filepath.Join(f.root, "inherited")
			created := object(f.call(0, "create", "--config", "analysis", "--path", second)["data"])
			if object(array(created["repos"])[0])["ref"] != nil {
				t.Fatal(created)
			}
		})
	}
}

func TestInvalidBaselineRefs(t *testing.T) {
	for _, ref := range []string{`null`, `{}`, `{"type":"tag","value":"v1"}`, `{"type":"commit","value":"abc123"}`, `{"type":"branch","value":"-bad"}`, `{"type":"branch","value":"a..b"}`, `{"type":"branch","value":"x","extra":1}`, `{"type":"branch","value":null}`, `{"type":"branch","type":"commit","value":"main"}`} {
		raw := []byte(`[{"repo":"api","target_dir":"api","ref":` + ref + `}]`)
		if checkJSON(raw) != nil {
			continue
		}
		if _, e := parseRefs(raw); e == nil {
			t.Fatalf("accepted %s", ref)
		}
	}
}
