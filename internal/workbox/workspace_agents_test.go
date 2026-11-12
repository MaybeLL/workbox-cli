package workbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOfflineWorkspaceManifestAndAgentEntry(t *testing.T) {
	f := setup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, project := range []string{"payments/api", "shared/contracts"} {
		group, name, _ := strings.Cut(project, "/")
		if name != "api" {
			f.remote(name)
		}
		if err := os.Mkdir(filepath.Join(f.remotes, group), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(f.remotes, name+".git"), filepath.Join(f.remotes, project+".git")); err != nil {
			t.Fatal(err)
		}
		f.call(0, "repo-config", "add", name, "--url", "https://example.test/"+project+".git", "--default-branch", "develop", "--summary", project)
	}
	f.workspace([]Ref{
		{Repo: "api", Target: "services/api", Ref: &BaselineRef{Type: "commit", Value: strings.Repeat("1", 40)}},
		{Repo: "contracts", Target: "contracts"},
	}, "")
	if err := os.Rename(f.remotes, f.remotes+"-offline"); err != nil {
		t.Fatal(err)
	}
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] != "check-ref-format" {
			t.Fatalf("unexpected git operation %v", args)
		}
		return nil
	}}
	root := filepath.Join(f.root, "workspace")
	plan := object(f.call(0, "plan", "--config", "analysis", "--path", root)["data"])
	assertAbsent(t, root)
	f.call(0, "create", "--config", "analysis", "--path", root)
	body, err := os.ReadFile(filepath.Join(root, ".workbox", "repos.json"))
	if err != nil {
		t.Fatal(err)
	}
	var repos []any
	if err := json.Unmarshal(body, &repos); err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || !reflect.DeepEqual(repos, plan["repos"]) {
		t.Fatalf("manifest differs from plan: %s", body)
	}
	for i, project := range []string{"payments/api", "shared/contracts"} {
		entry := object(repos[i])
		if entry["source_url"] != "https://example.test/"+project+".git" || entry["summary"] != project {
			t.Fatal(entry)
		}
		assertAbsent(t, filepath.Join(root, entry["target_dir"].(string)))
	}
	assertAbsent(t, filepath.Join(root, "AGENTS.md"))
	assertAbsent(t, filepath.Join(root, "services"))
	for _, path := range []string{filepath.Join(root, ".agents"), filepath.Join(root, ".opencode"), filepath.Join(home, ".agents")} {
		assertAbsent(t, path)
	}
}

func TestEmptyWorkspaceHasAgentEntry(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	root := filepath.Join(f.root, "empty")
	f.call(0, "plan", "--config", "analysis", "--path", root)
	assertAbsent(t, root)
	f.call(0, "create", "--config", "analysis", "--path", root)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{".workbox"}) {
		t.Fatalf("unexpected workspace entries: %v", names)
	}
}

func TestWorkspaceAgentEntryNeverOverwrites(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "workspace")
			tree, e := newTree(target)
			if e != nil {
				t.Fatal(e)
			}
			defer tree.root.Close()
			outside := filepath.Join(root, "outside.md")
			if err := os.WriteFile(outside, []byte("keep outside"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(target, "AGENTS.md")
			switch kind {
			case "file":
				err = os.WriteFile(path, []byte("keep existing rules"), 0644)
			case "symlink":
				err = os.Symlink(outside, path)
			case "directory":
				err = os.Mkdir(path, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := skillTree(t, root)
			if e := tree.writeFile("AGENTS.md", []byte("team rules")); e == nil || e.Code != "TARGET_EXISTS" {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, skillTree(t, root)) {
				t.Fatal("instruction creation overwrote an existing entry or link target")
			}
		})
	}
}

func TestWorkspaceReservedAgentTarget(t *testing.T) {
	for _, path := range []string{"AGENTS.md", "agents.MD/repo"} {
		if e := validateRefs([]Ref{{Repo: "api", Target: path}}); e == nil || e.Code != "CONFIG_INVALID" {
			t.Fatalf("accepted reserved target %q: %v", path, e)
		}
	}
	if e := validateRefs([]Ref{{Repo: "api", Target: "repos/AGENTS.md"}}); e != nil {
		t.Fatal("reserved a nested path unrelated to workspace instructions")
	}
	// Previously saved configurations are also rejected before filesystem writes.
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{Repo: "api", Target: "repos/api"}}, "")
	f.sql("UPDATE workspace_config SET repos=?", `[{"repo":"api","target_dir":"AGENTS.md"}]`)
	for _, action := range []string{"plan", "create"} {
		target := filepath.Join(f.root, action)
		result := f.call(2, action, "--config", "analysis", "--path", target)
		if errorCode(result) != "CONFIG_INVALID" {
			t.Fatal(result)
		}
		assertAbsent(t, target)
	}
}

func newTree(path string) (*ownedTree, *Failure) {
	parent, e := openInstanceParent(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	defer parent.Close()
	return newTreeAt(parent, path)
}
