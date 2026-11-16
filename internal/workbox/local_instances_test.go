package workbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstanceHistoryLifecycle(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	db := filepath.Join(os.Getenv("WORKBOX_STATE_HOME"), "instances.db")
	alias := filepath.Join(f.root, "alias")
	requireOK := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	requireOK(os.Symlink(f.root, alias))
	path := filepath.Join(f.root, "demand")
	viaAlias := filepath.Join(alias, "demand")
	list := func() []any { return array(object(f.call(0, "list")["data"])["instances"]) }
	if len(list()) != 0 {
		t.Fatal("nonempty initial list")
	}
	f.call(0, "plan", "--config", "analysis", "--path", viaAlias)
	assertAbsent(t, db)
	data := object(f.call(0, "create", "--config", "analysis", "--path", viaAlias)["data"])
	if data["workspace_path"] != path || object(data["registration"])["state"] != "registered" {
		t.Fatal(data)
	}
	checkStatus := func(status string) {
		t.Helper()
		rows := list()
		if len(rows) != 1 {
			t.Fatal(rows)
		}
		item := object(rows[0])
		if item["status"] != status || item["path"] != path || item["config_name"] != "analysis" {
			t.Fatal(item)
		}
		if _, err := time.Parse(time.RFC3339Nano, item["created_at"].(string)); err != nil {
			t.Fatal(item)
		}
		if status == "unknown" && item["reason"] == nil {
			t.Fatal(item)
		}
	}
	checkStatus("present")
	requireOK(os.Rename(path, path+"-moved"))
	checkStatus("missing")
	for _, cmd := range []string{"plan", "create"} {
		msg := object(f.call(2, cmd, "--config", "analysis", "--path", viaAlias)["error"])["message"].(string)
		if !strings.Contains(msg, "forget") || !strings.Contains(msg, path) {
			t.Fatal(msg)
		}
	}
	requireOK(os.WriteFile(path, []byte("unrelated file"), 0600))
	checkStatus("not_directory")
	requireOK(os.Remove(path))
	requireOK(os.Symlink(path, path))
	checkStatus("unknown")
	requireOK(os.Remove(path))
	// The alias's final component is missing. Its parent still identifies the record.
	f.call(0, "forget", "--path", viaAlias)
	if len(list()) != 0 {
		t.Fatal("stale record")
	}
	f.call(0, "create", "--config", "analysis", "--path", path)
	f.call(0, "forget", "--path", path)
	for _, cmd := range []string{"plan", "create"} {
		msg := object(f.call(2, cmd, "--config", "analysis", "--path", filepath.Join(viaAlias, "child"))["error"])["message"].(string)
		if !strings.Contains(msg, path) {
			t.Fatal(msg)
		}
	}
}

func TestForgetUsesRecordedPathBeforeSymlinksAndWithoutConfig(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	a, b := filepath.Join(f.root, "a"), filepath.Join(f.root, "b")
	for _, path := range []string{a, b} {
		f.call(0, "create", "--config", "analysis", "--path", path)
	}
	if err := os.Rename(a, a+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	f.app = &app{}
	t.Setenv("WORKBOX_CONFIG_HOME", filepath.Join(f.root, "uninitialized"))
	t.Setenv("WORKBOX_DB", "mysql")
	f.call(0, "forget", "--path", a)
	rows := array(object(f.call(0, "list")["data"])["instances"])
	if len(rows) != 1 || object(rows[0])["path"] != b {
		t.Fatal(rows)
	}
	for _, path := range []string{a, b} {
		if _, err := os.Stat(filepath.Join(path, ".workbox", "repos.json")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	f.call(0, "forget", "--path", b)
}

func TestRegistryFailureBeforeAndAfterPublication(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	root := filepath.Join(f.root, "task")
	dir := os.Getenv("WORKBOX_STATE_HOME")
	db := filepath.Join(dir, "instances.db")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"plan", "create"} {
		msg := object(f.call(1, cmd, "--config", "analysis", "--path", root)["error"])["message"].(string)
		if !strings.Contains(msg, db) {
			t.Fatal(msg)
		}
		assertAbsent(t, root)
	}
	f.call(1, "list")
	if err := os.Remove(db); err != nil {
		t.Fatal(err)
	}
	f.app.publishInstance = func(parent *os.Root, stage, name string) error {
		if err := publishDir(parent, stage, name); err != nil {
			return err
		}
		return os.Mkdir(db, 0700)
	}
	data := object(f.call(0, "create", "--config", "analysis", "--path", root)["data"])
	registration := object(data["registration"])
	if data["state"] != "prepared" || data["workspace_path"] != root || registration["state"] != "failed" || !strings.Contains(registration["reason"].(string), db) {
		t.Fatal(data)
	}
	if _, err := os.Stat(filepath.Join(root, ".workbox", "repos.json")); err != nil {
		t.Fatal(err)
	}
}
