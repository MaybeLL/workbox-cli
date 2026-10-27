package workbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
			shown := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
			if object(object(array(shown["repos"])[0])["ref"])["value"] != value {
				t.Fatal(shown)
			}
			export := filepath.Join(f.root, "export.json")
			f.call(0, "config", "export", "--output", export)
			f.call(0, "config", "import", "--input", export)
			f.call(0, "workspace-config", "remove", "analysis")
			f.call(0, "config", "import", "--input", export)
			target := filepath.Join(f.root, "workspace")
			plan := object(f.call(0, "workspace", "plan", "--config", "analysis", "--path", target)["data"])
			if object(array(plan["repos"])[0])["resolved_commit"] != sha {
				t.Fatal(plan)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("plan created target")
			}
			result := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", target)["data"])
			repo := object(array(result["repos"])[0])
			if repo["commit"] != sha || repo["default_branch"] != "develop" {
				t.Fatal(repo)
			}
			checkout := filepath.Join(target, "repos/api")
			if got := f.git(checkout, "rev-parse", "HEAD"); got != sha {
				t.Fatal(got)
			}
			if kind == "commit" {
				if repo["branch"] != nil || f.git(checkout, "rev-parse", "--abbrev-ref", "HEAD") != "HEAD" {
					t.Fatal(repo)
				}
			} else if repo["branch"] != "release" {
				t.Fatal(repo)
			}
			body, _ := os.ReadFile(filepath.Join(checkout, "README.txt"))
			if string(body) != "one\n" {
				t.Fatal(string(body))
			}
			original := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
			if original["default_branch"] != "develop" {
				t.Fatal(original)
			}
			// Omitting ref on update clears the override and restores inheritance.
			raw, _ := json.Marshal([]Ref{{Repo: "api", Target: "repos/api"}})
			f.call(0, "workspace-config", "update", "analysis", "--repos-file", f.file("clear.json", string(raw)))
			second := filepath.Join(f.root, "inherited")
			created := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", second)["data"])
			if object(array(created["repos"])[0])["branch"] != "develop" {
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

func TestUnavailableBaselineDoesNotCreateTarget(t *testing.T) {
	for _, kind := range []string{"branch", "commit"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			f.repo("api")
			value := "missing"
			if kind == "commit" {
				value = strings.Repeat("1", 40)
			}
			f.workspace([]Ref{{Repo: "api", Target: "api", Ref: &BaselineRef{Type: kind, Value: value}}}, "")
			code := 2
			if kind == "commit" {
				code = 1
			}
			for _, action := range []string{"plan", "create"} {
				target := filepath.Join(f.root, action)
				f.call(code, "workspace", action, "--config", "analysis", "--path", target)
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatal("failed preflight created target")
				}
			}
		})
	}
}

func TestBranchAdvanceKeepsCapturedBaseline(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{Repo: "api", Target: "api", Ref: &BaselineRef{Type: "branch", Value: "develop"}}}, "")
	source := filepath.Join(f.root, "api-source")
	captured := f.git(source, "rev-parse", "HEAD")
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] == "clone" {
			if err := os.WriteFile(filepath.Join(source, "new.txt"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			f.git(source, "add", ".")
			f.git(source, "commit", "-m", "Advance during create")
			f.git(source, "push", filepath.Join(f.remotes, "api.git"), "develop")
		}
		return nil
	}}
	target := filepath.Join(f.root, "workspace")
	result := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", target)["data"])
	repo := object(array(result["repos"])[0])
	if repo["commit"] != captured || repo["resolved_commit"] != captured {
		t.Fatal(repo)
	}
	if f.git(filepath.Join(target, "api"), "rev-parse", "HEAD") != captured {
		t.Fatal("baseline drifted")
	}
}

func TestCommitRefRejectsTagObject(t *testing.T) {
	f := setup(t)
	f.repo("api")
	remote := filepath.Join(f.remotes, "api.git")
	f.git(remote, "tag", "-a", "annotated", "-m", "tag object", "develop")
	sha := f.git(remote, "rev-parse", "annotated")
	f.workspace([]Ref{{Repo: "api", Target: "api", Ref: &BaselineRef{Type: "commit", Value: sha}}}, "")
	target := filepath.Join(f.root, "workspace")
	result := f.call(1, "workspace", "create", "--config", "analysis", "--path", target)
	if errorCode(result) != "GIT_ERROR" {
		t.Fatal(result)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("tag object created a workspace")
	}
}
