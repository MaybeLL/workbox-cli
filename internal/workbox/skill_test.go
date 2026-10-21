package workbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// An unusable DB location and nil Git runner establish that skill installation
// is independent of configuration storage and external Git commands.
func skillApp(t *testing.T) (*app, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	blocked := filepath.Join(home, "not-a-config-directory")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBOX_CONFIG_HOME", blocked)
	return &app{}, home
}

func skillCall(t *testing.T, a *app, code int, args ...string) map[string]any {
	t.Helper()
	var out, diag bytes.Buffer
	got := a.run(context.Background(), append(args, "--format", "json"), &out, &diag, "test")
	if got != code {
		t.Fatalf("exit=%d want=%d: %s %s", got, code, out.String(), diag.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result["ok"] != (code == 0) || len(result) != 3 {
		t.Fatalf("bad envelope: %s (%v)", out.String(), err)
	}
	return result
}

func skillTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			result[name] = "directory"
		case entry.Type()&os.ModeSymlink != 0:
			value, err := os.Readlink(path)
			result[name] = "link:" + value
			return err
		default:
			value, err := os.ReadFile(path)
			result[name] = "file:" + string(value)
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSkillBundle(t *testing.T, target string) {
	t.Helper()
	files, err := skillBundle()
	if err != nil {
		t.Fatal(err)
	}
	// Check required workflow resources in addition to the manifest. A manifest
	// without its referenced initialization/worktree instructions is incomplete.
	if len(files["SKILL.md"]) == 0 || len(files["references/agent-guide.md"]) == 0 {
		t.Fatal("required skill resources missing")
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if !sameSkill(root, files) {
		t.Fatal("installed bundle is incomplete or differs from embedded files")
	}
}

func TestSkillInstallDefaultAndRepeat(t *testing.T) {
	a, home := skillApp(t)
	target := filepath.Join(home, ".agents", "skills", "workbox")
	data := object(skillCall(t, a, 0, "skill", "install")["data"])
	if data["path"] != target || data["state"] != "installed" || data["skill"] != "workbox" {
		t.Fatal(data)
	}
	assertSkillBundle(t, target)
	manifest := filepath.Join(target, "SKILL.md")
	fixed := time.Unix(1234567890, 0)
	if err := os.Chtimes(manifest, fixed, fixed); err != nil {
		t.Fatal(err)
	}
	data = object(skillCall(t, a, 0, "skill", "install")["data"])
	info, err := os.Stat(manifest)
	if err != nil || !info.ModTime().Equal(fixed) || data["state"] != "unchanged" {
		t.Fatalf("repeat rewrote installation: %v %v", data, err)
	}
	if a.store.path != "" {
		t.Fatal("skill installation initialized the configuration store")
	}
}

func TestSkillInstallCustomPath(t *testing.T) {
	a, home := skillApp(t)
	data := object(skillCall(t, a, 0, "skill", "install", "--path", "~/custom skills/中文")["data"])
	target := filepath.Join(home, "custom skills", "中文", "workbox")
	if data["path"] != target {
		t.Fatal(data)
	}
	assertSkillBundle(t, target)
	if _, err := os.Stat(filepath.Join(home, ".agents")); !os.IsNotExist(err) {
		t.Fatal("custom installation also wrote the default directory")
	}
}

func TestSkillInstallConflictsPreserveExistingTree(t *testing.T) {
	for _, kind := range []string{"modified", "missing_reference", "extra_file", "extra_directory", "target_file", "empty_directory", "target_symlink", "reference_symlink"} {
		t.Run(kind, func(t *testing.T) {
			a, home := skillApp(t)
			dir := filepath.Join(home, "skills")
			target := filepath.Join(dir, "workbox")
			skillCall(t, a, 0, "skill", "install", "--path", dir)
			var err error
			switch kind {
			case "modified":
				err = os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("my own instructions"), 0600)
			case "missing_reference":
				err = os.Remove(filepath.Join(target, "references", "agent-guide.md"))
			case "extra_file":
				err = os.WriteFile(filepath.Join(target, "personal.md"), []byte("keep"), 0600)
			case "extra_directory":
				err = os.Mkdir(filepath.Join(target, "personal"), 0700)
			case "target_file", "empty_directory":
				if err = os.RemoveAll(target); err == nil {
					if kind == "target_file" {
						err = os.WriteFile(target, []byte("keep"), 0600)
					} else {
						err = os.Mkdir(target, 0700)
					}
				}
			case "target_symlink":
				outside := filepath.Join(home, "outside")
				if err = os.Rename(target, outside); err == nil {
					err = os.Symlink(outside, target)
				}
			case "reference_symlink":
				reference := filepath.Join(target, "references", "agent-guide.md")
				outside := filepath.Join(home, "outside.md")
				if err = os.Rename(reference, outside); err == nil {
					err = os.Symlink(outside, reference)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := skillTree(t, home)
			result := skillCall(t, a, 2, "skill", "install", "--path", dir)
			if errorCode(result) != "SKILL_CONFLICT" {
				t.Fatal(result)
			}
			if after := skillTree(t, home); !reflect.DeepEqual(before, after) {
				t.Fatal("conflicting installation changed existing files, links, or directories")
			}
		})
	}
}

func TestSkillInstallFailures(t *testing.T) {
	t.Run("parent_is_file", func(t *testing.T) {
		a, home := skillApp(t)
		blocked := filepath.Join(home, "not-a-config-directory")
		before := skillTree(t, home)
		result := skillCall(t, a, 1, "skill", "install", "--path", filepath.Join(blocked, "skills"))
		if errorCode(result) != "IO_ERROR" || !reflect.DeepEqual(before, skillTree(t, home)) {
			t.Fatal(result)
		}
	})
	t.Run("cancelled_before_install", func(t *testing.T) {
		_, home := skillApp(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		before := skillTree(t, home)
		_, e := installSkill(ctx, map[string]string{})
		if e == nil || e.Code != "INTERRUPTED" || !reflect.DeepEqual(before, skillTree(t, home)) {
			t.Fatal("cancelled install wrote files or returned the wrong error")
		}
	})
}

func TestSkillInstallArguments(t *testing.T) {
	for _, args := range [][]string{
		{"skill", "remove"}, {"skill", "install", "unexpected"},
		{"skill", "install", "--unknown", "value"}, {"skill", "install", "--path"},
		{"skill", "install", "--path", "a", "--path", "b"}, {"skill", "install", "--path", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, home := skillApp(t)
			before := skillTree(t, home)
			skillCall(t, a, 2, args...)
			if !reflect.DeepEqual(before, skillTree(t, home)) {
				t.Fatal("invalid command wrote files")
			}
		})
	}
}

func TestSkillInstallConcurrentPublish(t *testing.T) {
	_, home := skillApp(t)
	dir := filepath.Join(home, "skills")
	var wg sync.WaitGroup
	type outcome struct {
		data any
		err  *Failure
	}
	results := make(chan outcome, 8)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := installSkill(context.Background(), map[string]string{"path": dir})
			results <- outcome{data, err}
		}()
	}
	wg.Wait()
	close(results)
	installed := 0
	for result := range results {
		if result.err != nil {
			// A competitor can observe the empty destination reservation just
			// before publication; it must preserve that directory and report a conflict.
			if result.err.Code != "SKILL_CONFLICT" {
				t.Fatal(result.err)
			}
		} else if object(result.data)["state"] == "installed" {
			installed++
		}
	}
	if installed != 1 {
		t.Fatalf("installed=%d want=1", installed)
	}
	assertSkillBundle(t, filepath.Join(dir, "workbox"))
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "workbox" {
		t.Fatalf("staging files leaked: %v %v", entries, err)
	}
}
