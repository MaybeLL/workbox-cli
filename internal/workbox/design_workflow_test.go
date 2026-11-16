package workbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGuardedConfigUpdates(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			f := setup(t)
			f.app.store = factory.create(t)
			f.call(0, "workbox-config", "add", "example")
			db := storeDB(t, f.app.store)
			setVersion := "PRAGMA user_version=4"
			if factory.name == "mysql" {
				setVersion = "UPDATE workbox_schema SET version=4 WHERE id=1"
			}
			for _, stmt := range []string{"ALTER TABLE workspace_config DROP COLUMN generation", setVersion} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			original := f.revision("example")
			script := f.file("script.sh", "#!/bin/sh\necho new")
			args := []string{"workbox-config", "update", "example", "--init-script-file", script}
			f.call(2, args...)
			f.call(0, append(args, "--revision", original)...)
			changed := f.revision("example")
			if changed == original {
				t.Fatal("revision did not advance")
			}
			f.call(0, append(args, "--revision", changed)...)
			if f.revision("example") != changed {
				t.Fatal("no-op advanced revision")
			}
			f.call(2, append(args, "--revision", original)...)
			f.call(0, "workbox-config", "add", "other")
			otherRevision := f.revision("other")
			f.file("script.sh-next", "#!/bin/sh\necho next")
			var writers sync.WaitGroup
			for name, rev := range map[string]string{"example": changed, "other": otherRevision} {
				writers.Add(1)
				go func() {
					defer writers.Done()
					f.call(0, "workbox-config", "update", name, "--revision", rev, "--init-script-file", script+"-next")
				}()
			}
			writers.Wait()
			f.call(0, "workbox-config", "remove", "example")
			f.call(0, "workbox-config", "add", "example")
			f.call(2, append(args, "--revision", original)...)
		})
	}
}

func TestInstanceAppendAndResolve(t *testing.T) {
	f := setup(t)
	sha := strings.Repeat("a", 40)
	var advanced, deleted, offline atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case offline.Load():
			http.Error(w, "offline", 503)
		case strings.HasSuffix(r.URL.Path, "/commits/main"), strings.HasSuffix(r.URL.Path, "/commits/"+sha):
			fmt.Fprintf(w, `{"id":"%s"}`, sha) // The same-name tag survives deletion.
		case strings.HasSuffix(r.URL.Path, "/branches/main"):
			if deleted.Load() {
				http.NotFound(w, r)
				return
			}
			tip := sha
			if advanced.Load() {
				tip = strings.Repeat("b", 40)
			}
			fmt.Fprintf(w, `{"commit":{"id":"%s"}}`, tip)
		case strings.HasSuffix(r.URL.Path, "/files/f/raw") && r.URL.Query().Get("ref") == sha:
			fmt.Fprint(w, "pinned")
		case strings.Contains(r.URL.Path, "/repository/"):
			t.Errorf("unexpected source read: %s", r.URL.Path)
		default:
			fmt.Fprint(w, `{"default_branch":"main"}`)
		}
	}))
	defer server.Close()
	f.app.gitlabTransport = server.Client().Transport
	f.call(0, "workbox-config", "add", "test")
	dir := filepath.Join(f.root, "instance")
	f.call(0, "create", "--config", "test", "--path", dir)
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func() { defer wg.Done(); f.call(0, "add-repo", "--path", dir, "--url", server.URL+"/team/"+name) }()
	}
	wg.Wait()
	readManifest := func() string {
		raw, err := os.ReadFile(filepath.Join(dir, ".workbox", "repos.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	before := readManifest()
	var entries []workspaceRepo
	if json.Unmarshal([]byte(before), &entries) != nil || len(entries) != 2 {
		t.Fatal(before)
	}
	if object(f.call(0, "add-repo", "--path", dir, "--url", server.URL+"/team/one.git")["data"])["state"] != "unchanged" {
		t.Fatal("duplicate entry")
	}
	if err := os.Mkdir(filepath.Join(dir, "Docs"), 0700); err != nil {
		t.Fatal(err)
	}
	f.call(2, "add-repo", "--path", dir, "--url", server.URL+"/team/three", "--target-dir", "docs")
	assertAbsent(t, filepath.Join(dir, "repos"))
	if data := object(f.call(0, "resolve-ref", "--path", dir, "--repo", "one")["data"]); data["commit"] != sha || data["ref"] != "main" {
		t.Fatal(data)
	}
	advanced.Store(true)
	if data := object(f.call(0, "resolve-ref", "--path", dir, "--repo", "one")["data"]); data["commit"] != strings.Repeat("b", 40) {
		t.Fatal(data)
	}
	deleted.Store(true)
	for _, args := range [][]string{nil, {"--ref", "main"}} {
		cmd := []string{"resolve-ref", "--path", dir, "--repo", "one"}
		failure := object(f.call(1, append(cmd, args...)...)["error"])
		if object(failure["details"])["http_status"] != float64(404) {
			t.Fatal(failure)
		}
	}
	if got := object(f.call(0, "resolve-ref", "--path", dir, "--repo", "one", "--ref", sha)["data"]); got["commit"] != sha {
		t.Fatal(got)
	}
	for _, args := range [][]string{nil, {"--ref", "main"}} {
		cmd := []string{"read", "--path", dir, "--repo", "one", "--file", "f"}
		failure := object(f.call(1, append(cmd, args...)...)["error"])
		if object(failure["details"])["http_status"] != float64(404) {
			t.Fatal(failure)
		}
	}
	fixed := object(f.call(0, "read", "--path", dir, "--repo", "one", "--file", "f", "--ref", sha)["data"])
	if fixed["commit"] != sha || fixed["data"] != "pinned" {
		t.Fatal(fixed)
	}
	if readManifest() != before {
		t.Fatal("unexpected manifest rewrite")
	}
	url := server.URL + "/other/one"
	f.call(0, "add-repo", "--path", dir, "--url", url, "--name", "other-one")
	before = readManifest()
	offline.Store(true)
	data := object(f.call(0, "add-repo", "--path", dir, "--url", url)["data"])
	if data["state"] != "unchanged" || object(data["repo"])["name"] != "other-one" || before != readManifest() {
		t.Fatal(data)
	}
	f.call(2, "add-repo", "--path", dir, "--url", url, "--target-dir", "different")
	other := filepath.Join(f.root, "other")
	result := object(f.call(0, "create", "--path", other, "--config", "test")["data"])
	if len(array(result["repos"])) != 0 {
		t.Fatal(result)
	}
}

func TestUpgradeReceiptedSkill(t *testing.T) {
	_, home := skillApp(t)
	dir := filepath.Join(home, "skills")
	target := filepath.Join(dir, "workbox")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	old := map[string][]byte{"SKILL.md": []byte("old instructions")}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), old["SKILL.md"], 0600); err != nil {
		t.Fatal(err)
	}
	flags := map[string]string{"path": dir}
	if _, e := installSkill(context.Background(), flags); e == nil || e.Code != "SKILL_CONFLICT" {
		t.Fatal(e)
	}
	if e := recordSkill(dir, old); e != nil {
		t.Fatal(e)
	}
	data, e := installSkill(context.Background(), flags)
	if e != nil || object(data)["state"] != "upgraded" {
		t.Fatal(data, e)
	}
	assertSkillBundle(t, target)
	data, e = installSkill(context.Background(), flags)
	if e != nil || object(data)["state"] != "unchanged" {
		t.Fatal(data, e)
	}
	hashes, e := installedHashes(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := inspectRemoval(target, hashes); e != nil {
		t.Fatal(e)
	}
}

func TestSameRevisionConcurrentSelections(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			f := setup(t)
			f.app.store = factory.create(t)
			f.call(0, "workbox-config", "add", "team")
			for _, name := range []string{"first", "second"} {
				f.call(0, "skill-config", "add", "--installer", "npx-skills", "https://example.test/skills", "--skill", name)
			}
			revision := f.revision("team")
			type attempt struct {
				name string
				code int
				body string
			}
			done := make(chan attempt, 2)
			start := make(chan struct{})
			for _, name := range []string{"first", "second"} {
				go func() {
					<-start
					var out, diag bytes.Buffer
					code := f.app.run(context.Background(), []string{"workbox-config", "add-skill", "team", name, "--revision", revision, "--format", "json"}, &out, &diag, "test")
					done <- attempt{name, code, out.String()}
				}()
			}
			close(start)
			var winner, loser string
			for range 2 {
				a := <-done
				var res envelope
				if err := json.Unmarshal([]byte(a.body), &res); err != nil {
					t.Fatal(err)
				}
				switch {
				case a.code == 0 && res.OK:
					winner = a.name
				case a.code == 2 && res.Error != nil && res.Error.Code == "CONFIG_CONFLICT":
					loser = a.name
				default:
					t.Fatal(a)
				}
			}
			if winner == "" || loser == "" {
				t.Fatal("expected a winner and a stale writer", winner, loser)
			}
			w := object(object(f.call(0, "workbox-config", "show", "team")["data"])["workspace_config"])
			if got := array(w["skills"]); len(got) != 1 || got[0] != winner {
				t.Fatal(w)
			}
			w = object(object(f.call(0, "workbox-config", "add-skill", "team", loser, "--revision", w["revision"].(string))["data"])["workspace_config"])
			if got := array(w["skills"]); !slices.Equal(got, []any{winner, loser}) {
				t.Fatal(w)
			}
		})
	}
}

func TestImportedWorkboxWithMissingSkill(t *testing.T) {
	f := setup(t)
	f.call(0, "workbox-config", "add", "analysis")
	f.call(0, "skill-config", "add", "--installer", "npx-skills", "https://example.test/skills", "--skill", "grill-me")
	f.call(0, "workbox-config", "add-skill", "analysis", "grill-me", "--revision", f.revision("analysis"))
	incoming := filepath.Join(f.root, "original.json")
	f.call(0, "config", "export", "--output", incoming)

	// Exercise the removal example shipped with the installed Skill.
	files, err := skillBundle()
	if err != nil {
		t.Fatal(err)
	}
	guide := string(files["SKILL.md"])
	example := regexp.MustCompile("`(workbox [^`\\n]*remove-skill [^`\\n]*)`").FindStringSubmatch(guide)
	if len(example) != 2 {
		t.Fatal("missing executable Skill removal example")
	}
	args := strings.Fields(strings.NewReplacer("CONFIG", "analysis", "NAME", "grill-me", "REV", f.revision("analysis")).Replace(example[1]))
	if len(args) == 0 || args[0] != "workbox" {
		t.Fatal(example)
	}
	f.call(0, args[1:]...)
	f.call(0, "skill-config", "remove", "grill-me")

	if got := object(f.call(2, "config", "import", "--input", incoming)["error"])["code"]; got != "CONFIG_CONFLICT" {
		t.Fatal(got)
	}
	if got := object(f.call(2, "workbox-config", "add-skill", "analysis", "grill-me", "--revision", f.revision("analysis"))["error"])["code"]; got != "CONFIG_NOT_FOUND" {
		t.Fatal(got)
	}
	raw, err := os.ReadFile(incoming)
	if err != nil {
		t.Fatal(err)
	}
	var pkg Package
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	pkg.Workspaces = []Workspace{}
	dependencies, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	f.call(0, "config", "import", "--input", f.file("dependencies.json", string(dependencies)))
	f.call(0, "workbox-config", "add-skill", "analysis", "grill-me", "--revision", f.revision("analysis"))
	revision := f.revision("analysis")
	f.call(0, "config", "import", "--input", incoming)
	w := object(object(f.call(0, "workbox-config", "show", "analysis")["data"])["workspace_config"])
	if w["revision"] != revision || !slices.Equal(array(w["skills"]), []any{"grill-me"}) {
		t.Fatal(w)
	}
}

func TestUpgradeReportsPublishedFilesWhenReceiptFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only directory test requires an unprivileged process")
	}
	a, home := skillApp(t)
	dir := filepath.Join(home, "custom skills")
	target := filepath.Join(dir, "workbox")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	old := map[string][]byte{"SKILL.md": []byte("old")}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), old["SKILL.md"], 0600); err != nil {
		t.Fatal(err)
	}
	if e := recordSkill(dir, old); e != nil {
		t.Fatal(e)
	}
	state := os.Getenv("WORKBOX_STATE_HOME")
	if err := os.Chmod(state, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(state, 0700) })
	res := skillCall(t, a, 1, "skill", "install", "--path", dir)
	data := object(res["data"])
	if data["state"] != "upgraded" || data["publication"] != "published" || data["receipt"] != "failed" || data["path"] != target {
		t.Fatal(res)
	}
	assertSkillBundle(t, target)
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	retry := array(data["retry_args"])
	if len(retry) == 0 || retry[0] != "workbox" {
		t.Fatal(retry)
	}
	var args []string
	for _, arg := range retry[1:] {
		args = append(args, arg.(string))
	}
	if data := object(skillCall(t, a, 0, args...)["data"]); data["state"] != "unchanged" {
		t.Fatal(data)
	}
	hashes, e := installedHashes(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := inspectRemoval(target, hashes); e != nil {
		t.Fatal(e)
	}
}
