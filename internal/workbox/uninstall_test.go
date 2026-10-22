package workbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func uninstallFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected absent %s: %v", path, err)
	}
}

func uninstallFixture(t *testing.T) (string, string) {
	t.Helper()
	_, home := skillApp(t)
	binary := filepath.Join(home, "bin", "workbox")
	uninstallFile(t, binary, "executable fixture", 0755)
	return home, binary
}

func TestUninstallDirectDryRunAndRemoval(t *testing.T) {
	home, binary := uninstallFixture(t)
	for _, dir := range []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, "custom skills", "中文")} {
		if _, e := installSkill(context.Background(), map[string]string{"path": dir}); e != nil {
			t.Fatal(e)
		}
	}
	link := filepath.Join(home, "workbox-link")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	uninstallFile(t, filepath.Join(home, ".agents", "skills", "other", "SKILL.md"), "other skill", 0644)
	uninstallFile(t, filepath.Join(home, "config", "config.db"), "configuration", 0600)
	uninstallFile(t, filepath.Join(home, "workspace", "important.txt"), "work", 0600)
	uninstallFile(t, filepath.Join(home, "bin", "another-tool"), "tool", 0755)
	before := skillTree(t, home)
	data, e := uninstall(context.Background(), binary, link, map[string]string{"dry-run": "true"})
	if e != nil {
		t.Fatal(e)
	}
	plan := data.(*uninstallResult)
	if plan.State != "planned" || plan.CLI.Method != "direct" || plan.CLI.Link != link || plan.Configuration != "preserved" || plan.Workspaces != "preserved" {
		t.Fatal(plan)
	}
	if !reflect.DeepEqual(before, skillTree(t, home)) {
		t.Fatal("dry run wrote files")
	}
	data, e = uninstall(context.Background(), binary, link, nil)
	if e != nil || data.(*uninstallResult).State != "uninstalled" {
		t.Fatalf("%v %v", data, e)
	}
	for _, path := range []string{binary, link, filepath.Join(home, "state"), filepath.Join(home, ".agents", "skills", "workbox"), filepath.Join(home, "custom skills", "中文", "workbox")} {
		assertAbsent(t, path)
	}
	for _, path := range []string{".agents/skills/other/SKILL.md", "config/config.db", "workspace/important.txt", "bin/another-tool"} {
		if got, err := os.ReadFile(filepath.Join(home, path)); err != nil || "file:"+string(got) != before[path] {
			t.Fatalf("changed preserved file: %s %v", path, err)
		}
	}
}

func TestUninstallSkillConflictsArePreflighted(t *testing.T) {
	for _, kind := range []string{"modified", "missing", "extra", "directory", "symlink", "root_symlink"} {
		t.Run(kind, func(t *testing.T) {
			home, binary := uninstallFixture(t)
			for _, dir := range []string{"a-good", "z-conflict"} {
				if _, e := installSkill(context.Background(), map[string]string{"path": filepath.Join(home, dir)}); e != nil {
					t.Fatal(e)
				}
			}
			target := filepath.Join(home, "z-conflict", "workbox")
			switch kind {
			case "modified":
				uninstallFile(t, filepath.Join(target, "SKILL.md"), "user edits", 0644)
			case "missing":
				if err := os.Remove(filepath.Join(target, "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "extra":
				uninstallFile(t, filepath.Join(target, "personal.md"), "keep", 0644)
			case "directory":
				if err := os.Mkdir(filepath.Join(target, "personal"), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path := filepath.Join(target, "SKILL.md")
				outside := filepath.Join(home, "outside.md")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "root_symlink":
				outside := filepath.Join(home, "outside")
				if err := os.Rename(target, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			}
			before := skillTree(t, home)
			_, e := uninstall(context.Background(), binary, binary, nil)
			if e == nil || e.Code != "SKILL_CONFLICT" || e.Data.(*uninstallResult).State != "failed" {
				t.Fatal(e)
			}
			items := e.Data.(*uninstallResult).Skills
			if items[len(items)-1].State != "conflict" {
				t.Fatal("conflicting skill was not reported as a conflict")
			}
			if !reflect.DeepEqual(before, skillTree(t, home)) {
				t.Fatal("preflight failure changed files")
			}
		})
	}
}

func TestUninstallLegacyAndStaleReceipts(t *testing.T) {
	home, binary := uninstallFixture(t)
	dirs := []string{filepath.Join(home, ".codex", "skills"), filepath.Join(home, "legacy-custom"), filepath.Join(home, "stale")}
	for _, dir := range dirs {
		if _, e := installSkill(context.Background(), map[string]string{"path": dir}); e != nil {
			t.Fatal(e)
		}
	}
	state, _ := installStateDir()
	for _, dir := range dirs[:2] {
		canonical, _ := filepath.EvalSymlinks(dir)
		if err := os.Remove(filepath.Join(state, receiptName(filepath.Join(canonical, "workbox")))); err != nil {
			t.Fatal(err)
		}
	}
	// Move a previously recorded install away: a stale record is safe to clean.
	if err := os.Rename(filepath.Join(dirs[2], "workbox"), filepath.Join(home, "retained-backup")); err != nil {
		t.Fatal(err)
	}
	uninstallFile(t, filepath.Join(state, "other-state"), "keep", 0600)
	_, e := uninstall(context.Background(), binary, binary, map[string]string{"skill-path": dirs[1]})
	if e != nil {
		t.Fatal(e)
	}
	for _, dir := range dirs {
		assertAbsent(t, filepath.Join(dir, "workbox"))
	}
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 1 || entries[0].Name() != "other-state" {
		t.Fatalf("%v %v", entries, err)
	}
	assertSkillBundle(t, filepath.Join(home, "retained-backup"))
}

func TestUninstallHistoricalReceipt(t *testing.T) {
	home, binary := uninstallFixture(t)
	dir := filepath.Join(home, "old-skill")
	old := map[string][]byte{"SKILL.md": []byte("old manifest"), "references/old.md": []byte("old guide")}
	for name, body := range old {
		uninstallFile(t, filepath.Join(dir, "workbox", name), string(body), 0644)
	}
	if e := recordSkill(dir, old); e != nil {
		t.Fatal(e)
	}
	if _, e := uninstall(context.Background(), binary, binary, nil); e != nil {
		t.Fatal(e)
	}
	assertAbsent(t, filepath.Join(dir, "workbox"))
}

func TestUninstallInvalidRecordsPreserveAll(t *testing.T) {
	for _, kind := range []string{"malformed", "escape", "name", "version", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home, binary := uninstallFixture(t)
			dir := filepath.Join(home, "skills")
			if _, e := installSkill(context.Background(), map[string]string{"path": dir}); e != nil {
				t.Fatal(e)
			}
			canonical, _ := filepath.EvalSymlinks(dir)
			state, _ := installStateDir()
			path := filepath.Join(state, receiptName(filepath.Join(canonical, "workbox")))
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var r skillReceipt
			if err := json.Unmarshal(body, &r); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "malformed":
				body = []byte("{")
			case "escape":
				r.Files["../outside.md"] = r.Files["SKILL.md"]
				body, _ = json.Marshal(r)
			case "version":
				r.Version = 99
				body, _ = json.Marshal(r)
			case "name":
				path = filepath.Join(state, "skill-fake.json")
			case "symlink", "directory":
				if err := os.Rename(path, filepath.Join(home, "saved-record")); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					err = os.Symlink(filepath.Join(home, "saved-record"), path)
				} else {
					err = os.Mkdir(path, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind != "symlink" && kind != "directory" {
				uninstallFile(t, path, string(body), 0600)
			}
			before := skillTree(t, home)
			if _, e := uninstall(context.Background(), binary, binary, nil); e == nil || e.Code != "CONFIG_INVALID" {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, skillTree(t, home)) {
				t.Fatal("invalid record changed files")
			}
		})
	}
}

func TestSkillRecordsConcurrentAndAdoption(t *testing.T) {
	_, home := skillApp(t)
	var wg sync.WaitGroup
	errors := make(chan *Failure, 6)
	for _, name := range []string{"one", "two", "three", "four", "five", "six"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := installSkill(context.Background(), map[string]string{"path": filepath.Join(home, name)})
			errors <- e
		}()
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	candidates, state, records, e := skillCandidates("")
	if e != nil || len(records) != 6 || len(candidates) < 6 {
		t.Fatalf("%v %v %v", candidates, records, e)
	}
	canonical, _ := filepath.EvalSymlinks(filepath.Join(home, "one"))
	path := filepath.Join(state, receiptName(filepath.Join(canonical, "workbox")))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if data, e := installSkill(context.Background(), map[string]string{"path": canonical}); e != nil || object(data)["state"] != "unchanged" {
		t.Fatalf("%v %v", data, e)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("identical legacy install was not recorded")
	}
}

func TestSkillReceiptWriteFailureDoesNotPublish(t *testing.T) {
	_, home := skillApp(t)
	blocked := filepath.Join(home, "blocked-state")
	uninstallFile(t, blocked, "keep", 0600)
	t.Setenv("WORKBOX_STATE_HOME", blocked)
	dir := filepath.Join(home, "skills")
	if _, e := installSkill(context.Background(), map[string]string{"path": dir}); e == nil || e.Code != "IO_ERROR" {
		t.Fatal(e)
	}
	assertAbsent(t, filepath.Join(dir, "workbox"))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staged files leaked: %v %v", entries, err)
	}
}

func TestUninstallHomebrew(t *testing.T) {
	for _, scenario := range []string{"success", "failed", "wrong_tap", "wrong_cellar"} {
		t.Run(scenario, func(t *testing.T) {
			home, _ := uninstallFixture(t)
			prefix := filepath.Join(home, "brew-prefix")
			rack := filepath.Join(prefix, "Cellar", "workbox")
			binary := filepath.Join(rack, "0.1.2", "bin", "workbox")
			canonical, _ := filepath.EvalSymlinks(home)
			canonicalRack := filepath.Join(canonical, "brew-prefix", "Cellar", "workbox")
			uninstallFile(t, binary, "fixture", 0755)
			uninstallFile(t, filepath.Join(rack, "0.1.1", "bin", "workbox"), "old", 0755)
			tap := "maybell/tap"
			if scenario == "wrong_tap" {
				tap = "other/tap"
			}
			uninstallFile(t, filepath.Join(rack, "0.1.2", "INSTALL_RECEIPT.json"), `{"source":{"tap":"`+tap+`"}}`, 0644)
			// A process-boundary fake only moves its private test rack. No real brew is invoked.
			cellar := canonicalRack
			if scenario == "wrong_cellar" {
				cellar = home
			}
			body := "#!/bin/sh\nset -eu\nif [ \"$1\" = --cellar ]; then printf '%s\\n' \"$TEST_CELLAR\"; exit; fi\nprintf '%s\\n' \"$*\" > \"$TEST_BREW_LOG\"\n"
			if scenario == "failed" {
				body += "printf '%s\\n' 'dependency prevents uninstall' >&2\nexit 42\n"
			} else {
				body += "mv \"$TEST_RACK\" \"$TEST_RACK.removed\"\n"
			}
			uninstallFile(t, filepath.Join(prefix, "bin", "brew"), body, 0755)
			log := filepath.Join(home, "brew.log")
			t.Setenv("TEST_CELLAR", cellar)
			t.Setenv("TEST_BREW_LOG", log)
			t.Setenv("TEST_RACK", rack)
			if _, e := installSkill(context.Background(), nil); e != nil {
				t.Fatal(e)
			}
			before := skillTree(t, home)
			data, e := uninstall(context.Background(), binary, binary, nil)
			switch scenario {
			case "success":
				if e != nil || data.(*uninstallResult).CLI.Method != "homebrew" {
					t.Fatalf("%v %v", data, e)
				}
				assertAbsent(t, rack)
				assertAbsent(t, filepath.Join(home, ".agents", "skills", "workbox"))
			case "failed":
				if e == nil || e.Code != "UNINSTALL_FAILED" || e.Data.(*uninstallResult).CLI.State != "pending" {
					t.Fatal(e)
				}
				if _, err := os.Stat(binary); err != nil {
					t.Fatal(err)
				}
				assertAbsent(t, filepath.Join(home, ".agents", "skills", "workbox"))
			default:
				if e == nil || e.Code != "UNINSTALL_UNSUPPORTED" || !reflect.DeepEqual(before, skillTree(t, home)) {
					t.Fatal(e)
				}
			}
			if scenario == "success" || scenario == "failed" {
				args, err := os.ReadFile(log)
				if err != nil || strings.TrimSpace(string(args)) != "uninstall --formula --force maybell/tap/workbox" {
					t.Fatalf("%s %v", args, err)
				}
			}
		})
	}
}

func TestUninstallCancellationAndArguments(t *testing.T) {
	home, binary := uninstallFixture(t)
	before := skillTree(t, home)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := uninstall(ctx, binary, binary, nil); e == nil || e.Code != "INTERRUPTED" {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, skillTree(t, home)) {
		t.Fatal("cancelled uninstall wrote files")
	}
	for _, args := range [][]string{{"uninstall", "extra"}, {"uninstall", "--dry-run=wrong"}, {"uninstall", "--dry-run", "--dry-run"}, {"uninstall", "--skill-path"}, {"uninstall", "--unknown"}} {
		if _, e := parse(args); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"uninstall"}, {"uninstall", "--dry-run"}, {"uninstall", "--dry-run=false"}, {"uninstall", "--skill-path", "/tmp/skills"}} {
		if _, e := parse(args); e != nil {
			t.Fatalf("rejected %v: %v", args, e)
		}
	}
}

func TestUninstallRunningExecutable(t *testing.T) {
	home, binary := uninstallFixture(t)
	body, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, body, 0755); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command(binary, append(args, "--format", "json")...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil || result["ok"] != true {
			t.Fatalf("%s %v", out, err)
		}
		return object(result["data"])
	}
	call("skill", "install")
	call("skill", "install", "--path", filepath.Join(home, "custom"))
	if data := call("uninstall", "--dry-run"); data["state"] != "planned" {
		t.Fatal(data)
	}
	if data := call("uninstall"); data["state"] != "uninstalled" {
		t.Fatal(data)
	}
	for _, path := range []string{binary, filepath.Join(home, "state"), filepath.Join(home, ".agents", "skills", "workbox"), filepath.Join(home, "custom", "workbox")} {
		assertAbsent(t, path)
	}
	if _, err := os.Stat(testBinary); err != nil {
		t.Fatal("source executable was removed")
	}
}

func TestUninstallUnrecordedPreviousSkillBundle(t *testing.T) {
	home, binary := uninstallFixture(t)
	parent := filepath.Join(home, ".agents", "skills")
	for _, name := range []string{"SKILL.md", "references/agent-guide.md"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "skill-v0.2.0", name))
		if err != nil {
			t.Fatal(err)
		}
		uninstallFile(t, filepath.Join(parent, "workbox", name), string(raw), 0644)
	}
	_, e := uninstall(context.Background(), binary, binary, map[string]string{})
	if e != nil {
		t.Fatal(e)
	}
	assertAbsent(t, filepath.Join(parent, "workbox"))
	assertAbsent(t, binary)
}
