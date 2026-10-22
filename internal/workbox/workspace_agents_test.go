package workbox

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkspaceAgentEntryAndBaselineRules(t *testing.T) {
	f := setup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(f.root, "api-source")
	rules := "# API instructions\nRun the repository tests before delivery.\n"
	if err := os.WriteFile(filepath.Join(source, "AGENTS.md"), []byte(rules), 0644); err != nil {
		t.Fatal(err)
	}
	f.git(source, "add", "AGENTS.md")
	f.git(source, "commit", "-m", "Add repository rules")
	f.git(source, "push", filepath.Join(f.remotes, "api.git"), "develop")
	root := f.create()
	info, err := os.Lstat(filepath.Join(root, "AGENTS.md"))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 != 0 || info.Size() == 0 {
		t.Fatalf("workspace instructions missing or not a regular text file: %v %v", info, err)
	}
	base := filepath.Join(root, "repos", "api")
	actual, err := os.ReadFile(filepath.Join(base, "AGENTS.md"))
	if err != nil || string(actual) != rules || f.git(base, "status", "--porcelain") != "" || f.git(base, "rev-parse", "HEAD") != f.git(source, "rev-parse", "HEAD") {
		t.Fatal("workspace creation changed baseline instructions, files or HEAD")
	}
	for _, path := range []string{filepath.Join(root, ".agents"), filepath.Join(root, ".opencode"), filepath.Join(home, ".agents")} {
		assertAbsent(t, path)
	}
}

func TestEmptyWorkspaceHasAgentEntry(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	root := filepath.Join(f.root, "empty")
	f.call(0, "workspace", "plan", "--config", "analysis", "--path", root)
	assertAbsent(t, root)
	f.call(0, "workspace", "create", "--config", "analysis", "--path", root)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !reflect.DeepEqual(names, []string{".workbox", "AGENTS.md"}) {
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
			if e := tree.writeAgentInstructions(); e == nil || e.Code != "TARGET_EXISTS" {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, skillTree(t, root)) {
				t.Fatal("instruction creation overwrote an existing entry or link target")
			}
		})
	}
}

func TestWorkspaceAgentEntryReplacementDuringClone(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	target := filepath.Join(f.root, "workspace")
	outside := f.file("outside.md", "keep outside")
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] == "clone" {
			path := filepath.Join(target, "AGENTS.md")
			if err := os.Rename(path, filepath.Join(target, "AGENTS.saved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	response := f.call(2, "workspace", "create", "--config", "analysis", "--path", target)
	if errorCode(response) != "PATH_INVALID" || object(response["data"])["state"] != "failed" {
		t.Fatal(response)
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "keep outside" {
		t.Fatal("replacement link target changed")
	}
}

func TestWorkspaceReservedAgentTarget(t *testing.T) {
	for _, path := range []string{"AGENTS.md", "agents.MD/repo"} {
		if e := validateRefs([]Ref{{"api", path}}); e == nil || e.Code != "CONFIG_INVALID" {
			t.Fatalf("accepted reserved target %q: %v", path, e)
		}
	}
	if e := validateRefs([]Ref{{"api", "repos/AGENTS.md"}}); e != nil {
		t.Fatal("reserved a nested path unrelated to workspace instructions")
	}
	// Previously saved configurations are also rejected before filesystem writes.
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	f.sql("UPDATE workspace_config SET repos=?", `[{"repo":"api","target_dir":"AGENTS.md"}]`)
	for _, action := range []string{"plan", "create"} {
		target := filepath.Join(f.root, action)
		result := f.call(2, "workspace", action, "--config", "analysis", "--path", target)
		if errorCode(result) != "CONFIG_INVALID" {
			t.Fatal(result)
		}
		assertAbsent(t, target)
	}
}
