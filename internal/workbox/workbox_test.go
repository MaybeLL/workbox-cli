package workbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "workbox-go-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBinary = filepath.Join(dir, "workbox")
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-trimpath", "-o", testBinary, "../../cmd/workbox")
	cmd.Env = setEnv(os.Environ(), "CGO_ENABLED", "0")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintln(os.Stderr, string(out))
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type fixture struct {
	t                      *testing.T
	root, remotes, realGit string
	app                    *app
}

func setup(t *testing.T) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remotes := filepath.Join(root, "remotes")
	if err := os.Mkdir(remotes, 0755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, remotes: remotes, realGit: gitPath, app: &app{store: store{filepath.Join(root, "config", "config.db")}, gitRunner: gitCLI{}}}
	empty := f.file("gitconfig", "")
	for key, value := range map[string]string{"WORKBOX_CONFIG_HOME": filepath.Join(root, "config"), "GIT_CONFIG_GLOBAL": empty, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "url.file://" + filepath.ToSlash(remotes) + "/.insteadOf", "GIT_CONFIG_VALUE_0": "https://example.test/", "GIT_CONFIG_KEY_1": "protocol.file.allow", "GIT_CONFIG_VALUE_1": "always", "GIT_AUTHOR_NAME": "Test User", "GIT_AUTHOR_EMAIL": "test@example.test", "GIT_COMMITTER_NAME": "Test User", "GIT_COMMITTER_EMAIL": "test@example.test", "GIT_TERMINAL_PROMPT": "0"} {
		t.Setenv(key, value)
	}
	f.remote("api")
	return f
}
func (f *fixture) file(name, text string) string {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		f.t.Fatal(err)
	}
	return path
}
func (f *fixture) git(cwd string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command(f.realGit, args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func (f *fixture) remote(name string) string {
	f.t.Helper()
	source := filepath.Join(f.root, name+"-source")
	f.git(f.root, "init", "-b", "develop", source)
	if err := os.WriteFile(filepath.Join(source, "README.txt"), []byte("one\n"), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.git(source, "add", ".")
	f.git(source, "commit", "-m", "First")
	if err := os.WriteFile(filepath.Join(source, "README.txt"), []byte("two\n"), 0600); err != nil {
		f.t.Fatal(err)
	}
	f.git(source, "commit", "-am", "Second")
	f.git(source, "tag", "tag-only")
	f.git(f.root, "clone", "--bare", source, filepath.Join(f.remotes, name+".git"))
	return source
}
func object(v any) map[string]any { return v.(map[string]any) }
func array(v any) []any           { return v.([]any) }
func (f *fixture) call(code int, args ...string) map[string]any {
	f.t.Helper()
	var out, diag bytes.Buffer
	actual := f.app.run(context.Background(), append(args, "--format", "json"), &out, &diag, "0.1.0")
	if actual != code {
		f.t.Fatalf("%v: exit=%d want=%d stdout=%s stderr=%s", args, actual, code, out.String(), diag.String())
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		f.t.Fatalf("one JSON envelope expected: %v: %s", err, out.String())
	}
	if len(result) != 3 || result["ok"] != (code == 0) {
		f.t.Fatalf("bad envelope: %v", result)
	}
	return result
}
func (f *fixture) repo(name string) map[string]any {
	f.t.Helper()
	return object(object(f.call(0, "repo-config", "add", name, "--url", "https://example.test/"+name+".git", "--default-branch", "develop")["data"])["repo_config"])
}
func (f *fixture) workspace(refs []Ref, script string) map[string]any {
	f.t.Helper()
	raw, _ := json.Marshal(refs)
	repos := f.file("repos.json", string(raw))
	body := f.file("init.sh", script)
	return object(object(f.call(0, "workspace-config", "add", "analysis", "--repos-file", repos, "--init-script-file", body)["data"])["workspace_config"])
}
func (f *fixture) create() string {
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	root := filepath.Join(f.root, "workspace")
	f.call(0, "workspace", "create", "--config", "analysis", "--path", root)
	return root
}
func errorCode(result map[string]any) string { return object(result["error"])["code"].(string) }
func (f *fixture) sql(statement string, args ...any) {
	f.t.Helper()
	db, e := f.app.store.open(true)
	if e != nil {
		f.t.Fatal(e)
	}
	defer db.Close()
	if _, err := db.Exec(statement, args...); err != nil {
		f.t.Fatal(err)
	}
}

func TestEndToEnd(t *testing.T) {
	f := setup(t)
	r := f.repo("api")
	script := "#!/bin/sh\nprintf '%s\\n' ready > READY.txt\n"
	w := f.workspace([]Ref{{"api", "repos/api"}}, script)
	target := filepath.Join(f.root, "workspace")
	plan := object(f.call(0, "workspace", "plan", "--config", "analysis", "--path", target)["data"])
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("plan wrote target")
	}
	if object(object(array(plan["repos"])[0])["checks"])["ok"] != true {
		t.Fatal(plan)
	}
	prepared := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", target)["data"])
	repo := object(array(prepared["repos"])[0])
	if prepared["state"] != "prepared" || prepared["init_script"] != script || repo["branch"] != "develop" || len(repo["commit"].(string)) != 40 {
		t.Fatal(prepared)
	}
	base := filepath.Join(target, "repos", "api")
	if count := f.git(base, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("history truncated: %s", count)
	}
	if !realDirectory(filepath.Join(target, ".workbox", "worktree")) {
		t.Fatal("missing worktree directory")
	}
	if _, err := os.Stat(filepath.Join(target, "READY.txt")); !os.IsNotExist(err) {
		t.Fatal("CLI executed script")
	}
	before := f.git(base, "rev-parse", "HEAD")
	tmp := filepath.Join(target, ".workbox", "tmp")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "init.sh")
	if err := os.WriteFile(file, []byte(prepared["init_script"].(string)), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", file)
	cmd.Dir = target
	cmd.Env = setEnv(os.Environ(), "WORKBOX_WORKSPACE_ROOT", target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	artifact, _ := os.ReadFile(filepath.Join(target, "READY.txt"))
	if string(artifact) != "ready\n" || f.git(base, "rev-parse", "HEAD") != before || f.git(base, "status", "--porcelain") != "" {
		t.Fatal("artifact or baseline verification failed")
	}
	status := object(f.call(0, "workspace", "status", "--path", target)["data"])
	if object(array(status["repos"])[0])["clean"] != true {
		t.Fatal(status)
	}
	output := filepath.Join(f.root, "package.json")
	f.call(0, "config", "export", "--output", output)
	raw, _ := os.ReadFile(output)
	if bytes.Contains(raw, []byte(`"id"`)) {
		t.Fatal("portable package contains IDs")
	}
	f.app.store.path = filepath.Join(f.root, "fresh", "config.db")
	added := object(f.call(0, "config", "import", "--input", output)["data"])
	if len(array(object(added["added"])["repo_configs"])) != 1 {
		t.Fatal(added)
	}
	newW := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	newR := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if newW["id"] == w["id"] || newR["id"] == r["id"] {
		t.Fatal("import reused external IDs")
	}
	again := object(f.call(0, "config", "import", "--input", output)["data"])
	if len(array(object(again["added"])["repo_configs"])) != 0 {
		t.Fatal(again)
	}
	db, e := f.app.store.open(false)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if !reflect.DeepEqual(tables, []string{"repo_config", "workspace_config"}) {
		t.Fatal(tables)
	}
}
func TestMissingDBAndParser(t *testing.T) {
	f := setup(t)
	for _, entity := range []string{"repo-config", "workspace-config"} {
		f.call(0, entity, "list")
		for _, action := range []string{"show", "update", "remove"} {
			if errorCode(f.call(2, entity, action, "missing")) != "CONFIG_NOT_FOUND" {
				t.Fatal(action)
			}
		}
	}
	f.call(0, "config", "export", "--output", filepath.Join(f.root, "empty.json"))
	for _, action := range []string{"plan", "create"} {
		f.call(2, "workspace", action, "--config", "missing", "--path", filepath.Join(f.root, "new"))
	}
	for _, args := range [][]string{{"unknown"}, {"repo-config", "add", "api"}, {"workspace", "status", "--unknown", "x"}, {"config", "export"}} {
		if errorCode(f.call(2, args...)) != "INVALID_ARGUMENTS" {
			t.Fatal(args)
		}
	}
	for _, entity := range []string{"repo-config", "workspace-config", "workspace", "config"} {
		f.call(0, entity, "--help")
	}
	f.call(0, "--help")
	f.call(0, "--version")
	f.call(0, "workspace", "status", "--path", f.remotes)
	if _, err := os.Stat(f.app.store.path); !os.IsNotExist(err) {
		t.Fatal("read-only command initialized DB")
	}
}
func TestBranchesAndUnverifiableRemote(t *testing.T) {
	f := setup(t)
	for _, branch := range []string{"", "  ", "bad..name", "HEAD", "@{-1}", "-branch", "tag-only", "missing"} {
		t.Run(strings.ReplaceAll(branch, "/", "_"), func(t *testing.T) {
			result := f.call(2, "repo-config", "add", "api", "--url", "https://example.test/api.git", "--default-branch", branch)
			want := "CONFIG_INVALID"
			if branch == "tag-only" || branch == "missing" {
				want = "REMOTE_BRANCH_NOT_FOUND"
			}
			if errorCode(result) != want {
				t.Fatal(result)
			}
		})
	}
	if _, err := os.Stat(f.app.store.path); !os.IsNotExist(err) {
		t.Fatal("invalid add created database")
	}
	original := f.repo("api")
	if errorCode(f.call(1, "repo-config", "update", "api", "--url", "https://example.test/unavailable.git")) != "REMOTE_UNVERIFIABLE" {
		t.Fatal("ambiguous denial misclassified")
	}
	current := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if !reflect.DeepEqual(current, original) {
		t.Fatal("failed update changed config")
	}
}
func TestURLAndPathRules(t *testing.T) {
	for _, url := range []string{"https://example.test/team/api.git", "ssh://git@example.test:2222/team/api.git", "git@example.test:team/api.git", "git@example.test:/team/api.git"} {
		if e := validateURL(url); e != nil {
			t.Errorf("valid URL rejected: %s", url)
		}
	}
	for _, url := range []string{"https://secret@example.test/api.git", "https://user:secret@example.test/api.git", "ssh://user:secret@example.test/api.git", "https://example.test/api.git?token=secret", "file:///tmp/repo", "/tmp/repo", "C:repo"} {
		if e := validateURL(url); e == nil || strings.Contains(e.Error(), "secret") {
			t.Errorf("invalid URL accepted or echoed")
		}
	}
	for _, path := range []string{"", "/abs", "../x", "a/../b", "a/./b", "a//b", "a/", "a\\b", "C:/x", "a/.GIT/x", ".WORKBOX/x"} {
		if validateRefs([]Ref{{"a", path}}) == nil {
			t.Errorf("invalid path accepted: %q", path)
		}
	}
	for _, refs := range [][]Ref{{{"a", "repos/a"}, {"b", "repos/a/x"}}, {{"a", "repos/A"}, {"b", "repos/a"}}, {{"a", "repos/Straße"}, {"b", "repos/STRASSE"}}, {{"a", "a"}, {"a", "b"}}} {
		if validateRefs(refs) == nil {
			t.Errorf("overlap or duplicate accepted: %v", refs)
		}
	}
}
func TestConfigUpdatesAndReferences(t *testing.T) {
	f := setup(t)
	original := f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "#!/bin/sh\necho old\n")
	result := f.call(2, "repo-config", "remove", "api")
	if errorCode(result) != "CONFIG_CONFLICT" || len(array(object(result["error"])["details"].(map[string]any)["workspace_configs"])) != 1 {
		t.Fatal(result)
	}
	f.call(2, "repo-config", "update", "api")
	f.call(2, "workspace-config", "update", "analysis")
	updated := object(object(f.call(0, "repo-config", "update", "api", "--default-branch", "develop")["data"])["repo_config"])
	if updated["id"] != original["id"] {
		t.Fatal("ID changed")
	}
	before := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	for _, contents := range []string{`[{"repo":"missing","target_dir":"repos/x"}]`, `[{"repo":"api","target_dir":"repos/x","unknown":1}]`, `[{"repo":"api","repo":"api","target_dir":"repos/x"}]`, `null`} {
		file := f.file("bad.json", contents)
		f.call(2, "workspace-config", "update", "analysis", "--repos-file", file)
	}
	f.call(2, "workspace-config", "update", "analysis", "--init-script-file", f.file("bad.sh", "echo bad"))
	f.call(2, "workspace-config", "update", "analysis", "--init-script-file", f.file("bad.bin", string([]byte{255})))
	f.call(1, "workspace-config", "update", "analysis", "--repos-file", filepath.Join(f.root, "absent"))
	after := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	if !reflect.DeepEqual(before, after) {
		t.Fatal("invalid file changed config")
	}
	f.call(0, "workspace-config", "update", "analysis", "--init-script-file", f.file("empty.sh", ""))
	shown := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	if shown["init_script"] != "" || len(array(shown["repos"])) != 1 {
		t.Fatal(shown)
	}
	f.call(0, "workspace-config", "update", "analysis", "--repos-file", f.file("empty.json", "[]"))
	f.call(0, "repo-config", "remove", "api")
	f.call(0, "workspace-config", "remove", "analysis")
}
func TestScriptBytesAndExistingTargets(t *testing.T) {
	f := setup(t)
	script := "#!/bin/sh\r\n# café\r\nprintf ready\r\n"
	f.workspace([]Ref{}, script)
	shown := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	if shown["init_script"] != script {
		t.Fatal("script bytes changed")
	}
	target := filepath.Join(f.root, "existing")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"plan", "create"} {
		if errorCode(f.call(2, "workspace", action, "--config", "analysis", "--path", target)) != "TARGET_EXISTS" {
			t.Fatal(action)
		}
	}
	link := filepath.Join(f.root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f.call(2, "workspace", "create", "--config", "analysis", "--path", link)
	f.call(2, "workspace", "create", "--config", "analysis", "--path", filepath.Join(f.root, "missing", "new"))
	result := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", filepath.Join(f.root, "new"))["data"])
	if result["state"] != "prepared" || result["init_script"] != script || len(array(result["repos"])) != 0 {
		t.Fatal(result)
	}
}

type hookedGit struct {
	base   gitRunner
	before func([]string) *Failure
}

func (g hookedGit) run(ctx context.Context, cwd string, timeout time.Duration, remote bool, allowed []int, args ...string) (string, int, *Failure) {
	if e := g.before(args); e != nil {
		return "", -1, e
	}
	return g.base.run(ctx, cwd, timeout, remote, allowed, args...)
}

func TestOptimisticUpdateAndImport(t *testing.T) {
	for _, action := range []string{"update", "import"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			f.repo("api")
			f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
				if args[0] == "ls-remote" {
					f.sql("UPDATE repo_config SET default_branch='concurrent' WHERE name='api'")
				}
				return nil
			}}
			if action == "update" {
				if errorCode(f.call(2, "repo-config", "update", "api", "--default-branch", "develop")) != "CONFIG_CONFLICT" {
					t.Fatal("stale update")
				}
			} else {
				p := Package{Version: 1, Repos: []Repo{{Name: "api", URL: "https://example.test/api.git", Branch: "develop"}}, Workspaces: []Workspace{{Name: "new", Repos: []Ref{}}}}
				raw, _ := json.Marshal(p)
				if errorCode(f.call(2, "config", "import", "--input", f.file("import.json", string(raw)))) != "CONFIG_CONFLICT" {
					t.Fatal("stale import")
				}
				f.call(2, "workspace-config", "show", "new")
			}
			current := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
			if current["default_branch"] != "concurrent" {
				t.Fatal(current)
			}
		})
	}
}
func TestSnapshotChangesDuringClone(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "#!/bin/sh\necho old\n")
	target := filepath.Join(f.root, "new")
	f.call(0, "workspace", "plan", "--config", "analysis", "--path", target)
	f.call(0, "workspace-config", "update", "analysis", "--init-script-file", f.file("new.sh", "#!/bin/sh\necho snapshot\n"))
	source := filepath.Join(f.root, "api-source")
	if err := os.WriteFile(filepath.Join(source, "third"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	f.git(source, "add", ".")
	f.git(source, "commit", "-m", "Third")
	f.git(source, "push", filepath.Join(f.remotes, "api.git"), "develop")
	commit := f.git(source, "rev-parse", "HEAD")
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] == "clone" {
			f.sql("UPDATE workspace_config SET init_script=? WHERE name='analysis'", "#!/bin/sh\necho newer\n")
		}
		return nil
	}}
	result := object(f.call(0, "workspace", "create", "--config", "analysis", "--path", target)["data"])
	if result["init_script"] != "#!/bin/sh\necho snapshot\n" || object(array(result["repos"])[0])["commit"] != commit {
		t.Fatal(result)
	}
}
func TestPartialCloneFailureAndCancellation(t *testing.T) {
	for _, code := range []string{"GIT_ERROR", "INTERRUPTED"} {
		t.Run(code, func(t *testing.T) {
			f := setup(t)
			for _, name := range []string{"worker", "third"} {
				f.remote(name)
			}
			for _, name := range []string{"api", "worker", "third"} {
				f.repo(name)
			}
			f.workspace([]Ref{{"api", "repos/api"}, {"worker", "repos/worker"}, {"third", "repos/third"}}, "")
			f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
				if args[0] == "clone" && strings.Contains(strings.Join(args, " "), "worker.git") {
					return fail(code, "Injected failure")
				}
				return nil
			}}
			exit := 1
			if code == "INTERRUPTED" {
				exit = 130
			}
			target := filepath.Join(f.root, "partial")
			result := object(f.call(exit, "workspace", "create", "--config", "analysis", "--path", target)["data"])
			repos := array(result["repos"])
			for i, want := range []string{"completed", "failed", "not_attempted"} {
				if object(repos[i])["state"] != want {
					t.Fatal(result)
				}
			}
			if !realDirectory(filepath.Join(target, "repos", "api", ".git")) {
				t.Fatal("completed clone removed")
			}
		})
	}
}
func TestDirectoryRaces(t *testing.T) {
	f := setup(t)
	root := filepath.Join(f.root, "owned")
	tree, e := newTree(root)
	if e != nil {
		t.Fatal(e)
	}
	defer tree.root.Close()
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "repos")); err != nil {
		t.Fatal(err)
	}
	if e := tree.parents("repos/api"); e == nil {
		t.Fatal("unexpected link accepted")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("escaped target modified")
	}
	if _, e := newTree(root); e == nil || e.Code != "TARGET_EXISTS" {
		t.Fatal("existing root accepted")
	}
}
func TestImportValidationAndAtomicity(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	before, e := f.app.store.snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	base := `{"schema_version":1,"repo_configs":[{"name":"api","source_url":"https://example.test/api.git","default_branch":"develop"}],"workspace_configs":[{"name":"analysis","repos":[{"repo":"api","target_dir":"repos/api"}],"init_script":""}]}`
	for _, tc := range []struct {
		name, body, code string
		exit             int
	}{
		{"conflict", strings.Replace(base, `"init_script":""`, `"init_script":"#!/bin/sh\necho conflict"`, 1), "CONFIG_CONFLICT", 2},
		{"unknown", strings.Replace(base, `"name":"api"`, `"unknown":1,"name":"api"`, 1), "CONFIG_INVALID", 2},
		{"unknown_version", strings.Replace(base, `"schema_version":1`, `"schema_version":2`, 1), "SCHEMA_UNSUPPORTED", 2},
		{"bool_version", strings.Replace(base, `"schema_version":1`, `"schema_version":true`, 1), "SCHEMA_UNSUPPORTED", 2},
		{"duplicate_field", strings.Replace(base, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), "CONFIG_INVALID", 2},
		{"remote_failure", strings.Replace(base, `"repo_configs":[`, `"repo_configs":[{"name":"offline","source_url":"https://example.test/offline.git","default_branch":"develop"},`, 1), "REMOTE_UNVERIFIABLE", 1},
		{"dangling", `{"schema_version":1,"repo_configs":[],"workspace_configs":[{"name":"analysis","repos":[{"repo":"api","target_dir":"repos/api"}],"init_script":""}]}`, "CONFIG_INVALID", 2},
		{"null_refs", strings.Replace(base, `[{"repo":"api","target_dir":"repos/api"}]`, `null`, 1), "CONFIG_INVALID", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errorCode(f.call(tc.exit, "config", "import", "--input", f.file("invalid.json", tc.body))) != tc.code {
				t.Fatal(tc.name)
			}
			after, e := f.app.store.snapshot(context.Background())
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("partial import")
			}
		})
	}
	f.git(filepath.Join(f.remotes, "api.git"), "update-ref", "-d", "refs/heads/develop")
	if errorCode(f.call(2, "config", "import", "--input", f.file("same.json", base))) != "REMOTE_BRANCH_NOT_FOUND" {
		t.Fatal("no-op did not revalidate remote")
	}
}
func TestEmptyAndFailedImportDoNotInitializeDB(t *testing.T) {
	f := setup(t)
	f.call(0, "config", "import", "--input", f.file("empty.json", `{"schema_version":1,"repo_configs":[],"workspace_configs":[]}`))
	f.call(1, "config", "import", "--input", f.file("bad.json", `{"schema_version":1,"repo_configs":[{"name":"offline","source_url":"https://example.test/offline.git","default_branch":"develop"}],"workspace_configs":[]}`))
	if _, err := os.Stat(f.app.store.path); !os.IsNotExist(err) {
		t.Fatal("failed import created DB")
	}
}
func TestExportNoOverwrite(t *testing.T) {
	f := setup(t)
	existing := f.file("export.json", "keep")
	f.call(2, "config", "export", "--output", existing)
	body, _ := os.ReadFile(existing)
	if string(body) != "keep" {
		t.Fatal("existing output overwritten")
	}
	link := filepath.Join(f.root, "link.json")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	f.call(2, "config", "export", "--output", link)
	newFile := filepath.Join(f.root, "new.json")
	f.call(0, "config", "export", "--output", newFile)
	var parsed Package
	raw, _ := os.ReadFile(newFile)
	if json.Unmarshal(raw, &parsed) != nil || parsed.Version != 1 || len(parsed.Repos) != 0 {
		t.Fatal(string(raw))
	}
	leftovers, _ := filepath.Glob(filepath.Join(f.root, ".workbox-export-*"))
	if len(leftovers) > 0 {
		t.Fatal(leftovers)
	}
}
func TestSchemaCorruptionAndLegacyDatabase(t *testing.T) {
	f := setup(t)
	f.sql("PRAGMA user_version=9")
	if errorCode(f.call(2, "repo-config", "list")) != "SCHEMA_UNSUPPORTED" {
		t.Fatal("unsupported schema")
	}
	f.call(0, "workspace", "status", "--path", f.remotes)
	if err := os.WriteFile(f.app.store.path, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if errorCode(f.call(1, "repo-config", "list")) != "IO_ERROR" {
		t.Fatal("corrupt DB")
	}
	if err := os.Remove(f.app.store.path); err != nil {
		t.Fatal(err)
	}
	f.sql("CREATE TABLE repo_config (id TEXT PRIMARY KEY NOT NULL,name TEXT UNIQUE NOT NULL,source_url TEXT NOT NULL,default_branch TEXT NOT NULL)")
	f.sql("CREATE TABLE workspace_config (id TEXT PRIMARY KEY NOT NULL,name TEXT UNIQUE NOT NULL,repos TEXT NOT NULL,init_script TEXT NOT NULL DEFAULT '')")
	f.sql("PRAGMA user_version=1")
	f.sql("INSERT INTO repo_config VALUES (?,?,?,?)", "legacy-repo", "api", "https://example.test/api.git", "develop")
	f.sql("INSERT INTO workspace_config VALUES (?,?,?,?)", "legacy-workspace", "analysis", `[{"repo_config_id":"legacy-repo","target_dir":"repos/api"}]`, "#!/bin/sh\r\necho café\r\n")
	shown := object(object(f.call(0, "workspace-config", "show", "analysis")["data"])["workspace_config"])
	if shown["id"] != "legacy-workspace" || object(array(shown["repos"])[0])["repo"] != "api" {
		t.Fatal(shown)
	}
	f.call(0, "repo-config", "update", "api", "--default-branch", "develop")
	updated := object(object(f.call(0, "repo-config", "show", "api")["data"])["repo_config"])
	if updated["id"] != "legacy-repo" {
		t.Fatal(updated)
	}
}
func TestConfigHomePrecedence(t *testing.T) {
	f := setup(t)
	t.Setenv("WORKBOX_CONFIG_HOME", "relative-config")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "xdg"))
	path, e := configPath()
	if e != nil || !strings.HasSuffix(path, "/relative-config/config.db") {
		t.Fatal(path, e)
	}
	t.Setenv("WORKBOX_CONFIG_HOME", "")
	path, e = configPath()
	if e != nil || path != filepath.Join(f.root, "xdg", "workbox", "config.db") {
		t.Fatal(path, e)
	}
}

func TestConcurrentExportPublishesOneCompleteFile(t *testing.T) {
	f := setup(t)
	target := filepath.Join(f.root, "concurrent.json")
	results := make(chan *Failure, 8)
	for i := 0; i < 8; i++ {
		go func() { _, e := f.app.export(context.Background(), target); results <- e }()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		e := <-results
		if e == nil {
			successes++
		} else if e.Code != "TARGET_EXISTS" {
			t.Fatal(e)
		}
	}
	if successes != 1 {
		t.Fatalf("%d successful publications", successes)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var p Package
	if json.Unmarshal(raw, &p) != nil || p.Version != 1 {
		t.Fatal("published incomplete JSON")
	}
	leftovers, _ := filepath.Glob(filepath.Join(f.root, ".workbox-export-*"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}
func TestRootAppearingAfterPreflight(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	target := filepath.Join(f.root, "racing")
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] == "ls-remote" {
			if err := os.Mkdir(target, 0755); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	response := f.call(2, "workspace", "create", "--config", "analysis", "--path", target)
	if errorCode(response) != "TARGET_EXISTS" || object(array(object(response["data"])["repos"])[0])["state"] != "not_attempted" {
		t.Fatal(response)
	}
	children, _ := os.ReadDir(target)
	if len(children) != 0 {
		t.Fatal("unexpected target written")
	}
}

func TestParentLinkedAfterPreflightDoesNotEscape(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api"}}, "")
	parent := filepath.Join(f.root, "parent")
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	f.app.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
		if args[0] == "ls-remote" {
			if err := os.Rename(parent, parent+"-old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, parent); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	response := f.call(2, "workspace", "create", "--config", "analysis", "--path", filepath.Join(parent, "new"))
	if errorCode(response) != "PATH_INVALID" {
		t.Fatal(response)
	}
	children, _ := os.ReadDir(outside)
	if len(children) != 0 {
		t.Fatal("linked parent escaped")
	}
}
func TestJSONUnicodeAndNullContracts(t *testing.T) {
	for _, raw := range []string{`{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0041"}`} {
		if checkJSON([]byte(raw)) == nil {
			t.Fatalf("unpaired surrogate accepted: %s", raw)
		}
	}
	for _, raw := range []string{`{"x":"\ud83d\ude00"}`, `{"x":"\\ud800"}`} {
		if e := checkJSON([]byte(raw)); e != nil {
			t.Fatalf("valid Unicode rejected: %s: %v", raw, e)
		}
	}
	f := setup(t)
	for _, body := range []string{"#!\rprintf invalid", "#! \n", "\n#!/bin/sh\n"} {
		if validateWorkspace(Workspace{Name: "empty", Repos: []Ref{}, Script: body}) == nil {
			t.Fatalf("invalid first-line shebang accepted: %q", body)
		}
	}
	for _, raw := range []string{`{"schema_version":1,"repo_configs":null,"workspace_configs":[]}`, `{"schema_version":1,"repo_configs":[],"workspace_configs":[{"name":"x","repos":[],"init_script":null}]}`} {
		f.call(2, "config", "import", "--input", f.file("invalid.json", raw))
	}
}

func TestWhitespacePathsRemainExact(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.workspace([]Ref{{"api", "repos/api "}}, "")
	root := filepath.Join(f.root, "workspace ")
	f.call(0, "workspace", "create", "--config", "analysis", "--path", root)
	result := object(f.call(0, "workspace", "status", "--path", root)["data"])
	if result["workspace_path"] != root || object(array(result["repos"])[0])["path"] != "repos/api " {
		t.Fatal(result)
	}
}

func TestStatusDirtyDetachedUnbornAndReadOnlyIndex(t *testing.T) {
	f := setup(t)
	root := f.create()
	base := filepath.Join(root, "repos", "api")
	index := filepath.Join(base, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	entry := func() *map[string]any {
		result := object(f.call(0, "workspace", "status", "--path", root)["data"])
		row := object(array(result["repos"])[0])
		return &row
	}
	if (*entry())["clean"] != true {
		t.Fatal("clean clone dirty")
	}
	after, _ := os.ReadFile(index)
	now, _ := os.Stat(index)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(now.ModTime()) {
		t.Fatal("status refreshed index")
	}
	untracked := filepath.Join(base, "untracked")
	if err := os.WriteFile(untracked, []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if (*entry())["clean"] != false {
		t.Fatal("untracked not dirty")
	}
	if err := os.Remove(untracked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "README.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	if (*entry())["clean"] != false {
		t.Fatal("unstaged not dirty")
	}
	f.git(base, "add", ".")
	if (*entry())["clean"] != false {
		t.Fatal("staged not dirty")
	}
	f.git(base, "reset", "--hard", "HEAD")
	f.git(base, "checkout", "--detach")
	if (*entry())["branch"] != nil {
		t.Fatal("detached branch invented")
	}
	var human, diag bytes.Buffer
	if code := f.app.run(context.Background(), []string{"workspace", "status", "--path", root}, &human, &diag, "0.1.0"); code != 0 || !strings.Contains(human.String(), "detached HEAD") {
		t.Fatal(human.String(), diag.String())
	}
	f.git(root, "init", "-b", "new", filepath.Join(root, "unborn"))
	result := object(f.call(0, "workspace", "status", "--path", root)["data"])
	for _, row := range array(result["repos"]) {
		r := object(row)
		if r["path"] == "unborn" && (r["branch"] != "new" || r["commit"] != nil || r["clean"] != true) {
			t.Fatal(r)
		}
	}
}
func TestStatusWorktreeOwnershipAndBrokenCandidates(t *testing.T) {
	f := setup(t)
	root := f.create()
	base := filepath.Join(root, "repos", "api")
	commit := f.git(base, "rev-parse", "HEAD")
	container := filepath.Join(root, ".workbox", "worktree")
	wt := filepath.Join(container, "feature")
	f.git(base, "worktree", "add", "-b", "feature", wt, commit)
	result := object(f.call(0, "workspace", "status", "--path", root)["data"])
	repos := array(result["repos"])
	if len(repos) != 2 || object(repos[0])["kind"] != "worktree" || object(repos[0])["commit"] != commit {
		t.Fatal(result)
	}
	if f.git(base, "rev-parse", "HEAD") != commit || f.git(base, "status", "--porcelain") != "" {
		t.Fatal("worktree creation changed baseline files or HEAD")
	}
	f.git(filepath.Join(f.root, "api-source"), "worktree", "add", "-b", "foreign", filepath.Join(container, "foreign"))
	if err := os.Mkdir(filepath.Join(container, "broken"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "api-source"), filepath.Join(container, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(container, "file"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	response := f.call(1, "workspace", "status", "--path", root)
	if errorCode(response) != "STATUS_PARTIAL" {
		t.Fatal(response)
	}
	result = object(response["data"])
	errors := 0
	for _, row := range array(result["repos"]) {
		r := object(row)
		if r["error"] != nil {
			errors++
			if r["clean"] != nil || r["branch"] != nil || r["commit"] != nil {
				t.Fatal(r)
			}
		}
	}
	if errors != 4 {
		t.Fatal(result)
	}
}
func TestStatusSubmoduleAndIgnoredFiles(t *testing.T) {
	f := setup(t)
	root := f.create()
	base := filepath.Join(root, "repos", "api")
	f.git(base, "submodule", "add", filepath.Join(f.remotes, "api.git"), "deps/sub")
	f.git(base, "commit", "-am", "Add submodule")
	result := object(f.call(0, "workspace", "status", "--path", root)["data"])
	if len(array(result["repos"])) != 1 || object(array(result["repos"])[0])["clean"] != true {
		t.Fatal(result)
	}
	dirty := filepath.Join(base, "deps", "sub", "untracked")
	if err := os.WriteFile(dirty, []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	result = object(f.call(0, "workspace", "status", "--path", root)["data"])
	if object(array(result["repos"])[0])["clean"] != false {
		t.Fatal(result)
	}
	if err := os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".git", "info", "exclude"), []byte("ignored\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "ignored"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	result = object(f.call(0, "workspace", "status", "--path", root)["data"])
	if object(array(result["repos"])[0])["clean"] != true {
		t.Fatal(result)
	}
}
func TestUnreadablePathsAndAbnormalContainer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem mode permissions")
	}
	f := setup(t)
	root := f.create()
	configDir := filepath.Dir(f.app.store.path)
	if err := os.Chmod(configDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(configDir, 0700) })
	if errorCode(f.call(1, "repo-config", "list")) != "IO_ERROR" {
		t.Fatal("unreadable DB treated as missing")
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(root, ".workbox")
	if err := os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(hidden, 0700) })
	result := f.call(1, "workspace", "status", "--path", root)
	if errorCode(result) != "STATUS_PARTIAL" || len(array(object(result["data"])["scan_errors"])) == 0 {
		t.Fatal(result)
	}
	if err := os.Chmod(hidden, 0700); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(denied, 0700) })
	result = f.call(1, "workspace", "status", "--path", root)
	if len(array(object(result["data"])["scan_errors"])) != 1 {
		t.Fatal(result)
	}
	if err := os.Chmod(denied, 0700); err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(hidden, "worktree")
	if err := os.Remove(container); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.remotes, container); err != nil {
		t.Fatal(err)
	}
	f.call(1, "workspace", "status", "--path", root)
}
func TestTimeoutAndSecretDiagnostics(t *testing.T) {
	f := setup(t)
	bin := filepath.Join(f.root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(bin, "git")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, _, e := (gitCLI{}).run(context.Background(), "", 30*time.Millisecond, true, []int{0}, "ls-remote")
	if e == nil || e.Code != "TIMEOUT" {
		t.Fatal(e)
	}
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho secret-token >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, _, e = (gitCLI{}).run(context.Background(), "", time.Second, true, []int{0}, "ls-remote")
	raw, _ := json.Marshal(e)
	if e == nil || e.Code != "REMOTE_UNVERIFIABLE" || bytes.Contains(raw, []byte("secret-token")) {
		t.Fatal(string(raw))
	}
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func TestRealSignalsDuringClone(t *testing.T) {
	f := setup(t)
	f.remote("worker")
	f.repo("api")
	f.repo("worker")
	f.workspace([]Ref{{"api", "repos/api"}, {"worker", "repos/worker"}}, "")
	bin := filepath.Join(f.root, "signal-bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(f.root, "started")
	wrapper := "#!/bin/sh\ncase \"$*\" in\n  *clone*worker.git*) touch " + shellQuote(marker) + "; sleep 30 ;;\nesac\nexec " + shellQuote(f.realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			os.Remove(marker)
			target := filepath.Join(f.root, fmt.Sprintf("signal-%d", sig))
			cmd := exec.Command(testBinary, "workspace", "create", "--config", "analysis", "--path", target, "--format", "json")
			cmd.Env = setEnv(os.Environ(), "PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			})
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if exists, _ := pathExists(marker); exists {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			exists, _ := pathExists(marker)
			if !exists {
				t.Fatal("second clone did not start")
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 130 {
					t.Fatalf("exit: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("signal did not cancel Git")
			}
			var result map[string]any
			if json.Unmarshal(stdout.Bytes(), &result) != nil || errorCode(result) != "INTERRUPTED" {
				t.Fatal(stdout.String())
			}
			repos := array(object(result["data"])["repos"])
			if object(repos[0])["state"] != "completed" || object(repos[1])["state"] != "failed" {
				t.Fatal(result)
			}
		})
	}
}
