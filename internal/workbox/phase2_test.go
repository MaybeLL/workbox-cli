package workbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTeamDocsAtomicPublication(t *testing.T) {
	f := setup(t)
	sha := strings.Repeat("a", 40)
	branches := 0
	broken := true
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-personal" {
			t.Error("missing credential")
		}
		switch r.URL.Path {
		case "/api/v4/projects/team/rules/repository/branches/main":
			branches++
			fmt.Fprintf(w, `{"commit":{"id":"%s"}}`, sha)
		case "/api/v4/projects/team/rules/repository/files/index.md/raw", "/api/v4/projects/team/rules/repository/files/review.md/raw":
			if r.URL.Query().Get("ref") != sha {
				t.Error("unpinned raw request")
			}
			if broken && strings.Contains(r.URL.Path, "review") {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, "# Team\r\n")
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	f.app.gitlabTransport = server.Client().Transport
	host := strings.TrimPrefix(server.URL, "https://")
	f.call(0, "init", "--backend", "sqlite", "--skip-skill", "--gitlab-host", host, "--gitlab-token-file", f.file("token", "test-personal"))
	docs := []Document{{server.URL + "/team/rules", BaselineRef{"branch", "main"}, "index.md", "Docs/first.md"}, {server.URL + "/team/rules", BaselineRef{"branch", "main"}, "review.md", "docs/review.md"}}
	raw, _ := json.Marshal(docs)
	f.call(0, "workbox-config", "add", "analysis", "--docs-file", f.file("docs.json", string(raw)))
	target := filepath.Join(f.root, "demand")
	f.call(0, "plan", "--config", "analysis", "--path", target)
	if branches != 0 {
		t.Fatal("plan contacted server")
	}
	response := f.call(1, "create", "--config", "analysis", "--path", target)
	if object(object(response["error"])["details"])["target_path"] != "docs/review.md" {
		t.Fatal(response)
	}
	assertAbsent(t, target)
	broken = false
	f.call(0, "create", "--config", "analysis", "--path", target)
	if branches != 2 {
		t.Fatalf("branch resolved %d times across two creates", branches)
	}
	for _, d := range docs {
		b, err := os.ReadFile(filepath.Join(target, d.Target))
		if err != nil || string(b) != "# Team\r\n" {
			t.Fatal(d, err)
		}
	}
	assertAbsent(t, filepath.Join(target, ".agents"))
	docs[1].Target = ".agents/skills/bad.md"
	raw, _ = json.Marshal(docs)
	f.call(2, "workbox-config", "update", "analysis", "--docs-file", f.file("bad.json", string(raw)))
}
func TestSkillOnlyOfflineRoundTrip(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			f := setup(t)
			f.app.store = factory.create(t)
			register := []string{"skill-config", "add", "--installer", "npx-skills", "https://github.com/mattpocock/skills", "--skill", "grill-me"}
			f.call(0, register...)
			f.call(0, register...)
			pkg := filepath.Join(f.root, "skills.json")
			f.call(0, "config", "export", "--output", pkg)
			f.call(0, "skill-config", "remove", "grill-me")
			t.Setenv("PATH", t.TempDir())
			f.call(0, "config", "import", "--input", pkg)
			f.call(0, "config", "import", "--input", pkg)
			result := object(f.call(0, "skill-config", "show", "grill-me")["data"])
			if result["url"] != register[4] {
				t.Fatal(result)
			}
			f.workspace([]Ref{}, "")
			f.call(0, "workbox-config", "add-skill", "analysis", "grill-me")
			f.call(2, "skill-config", "remove", "grill-me")
			target := filepath.Join(f.root, "missing-npx")
			f.call(1, "create", "--config", "analysis", "--path", target)
			assertAbsent(t, target)
			f.call(0, "workbox-config", "remove-skill", "analysis", "grill-me")
			f.call(0, "create", "--config", "analysis", "--path", target)
			assertAbsent(t, filepath.Join(target, ".agents"))
		})
	}
}

func TestGitSkillSelection(t *testing.T) {
	for _, allowFilter := range []string{"true", "false"} {
		t.Run("allowFilter="+allowFilter, func(t *testing.T) {
			f := setup(t)
			f.git(filepath.Join(f.remotes, "api.git"), "config", "uploadpack.allowFilter", allowFilter)
			src := filepath.Join(f.root, "api-source")
			if err := os.MkdirAll(filepath.Join(src, "guides", "refs"), 0755); err != nil {
				t.Fatal(err)
			}
			for rel, body := range map[string]string{"SKILL.md": "---\nname: fixture\ndescription: Example\n---\n", "refs/note": "unchanged"} {
				if err := os.WriteFile(filepath.Join(src, "guides", rel), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			// An unrelated file has a required checkout filter unavailable to this machine.
			// Installing only guides must work without materializing it.
			for rel, data := range map[string]string{".gitattributes": "unrelated filter=not-installed\n", "unrelated": "other team data\n"} {
				if err := os.WriteFile(filepath.Join(src, rel), []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			f.git(src, "add", ".gitattributes", "unrelated")
			f.git(src, "add", "guides")
			f.git(src, "commit", "-m", "skill")
			f.git(src, "push", filepath.Join(f.remotes, "api.git"), "develop")
			t.Setenv("GIT_CONFIG_COUNT", "4")
			t.Setenv("GIT_CONFIG_KEY_2", "filter.not-installed.required")
			t.Setenv("GIT_CONFIG_VALUE_2", "true")
			t.Setenv("GIT_CONFIG_KEY_3", "filter.not-installed.smudge")
			t.Setenv("GIT_CONFIG_VALUE_3", "false")
			f.call(0, "skill-config", "add", "--git-url", "https://example.test/api.git", "--subdir", "guides", "--branch", "develop")
			f.workspace([]Ref{}, "")
			f.call(0, "workbox-config", "add-skill", "analysis", "fixture")
			root := filepath.Join(f.root, "demand")
			f.call(0, "create", "--config", "analysis", "--path", root)
			b, err := os.ReadFile(filepath.Join(root, ".agents/skills/fixture/refs/note"))
			if err != nil || string(b) != "unchanged" {
				t.Fatal(err)
			}
		})
	}
}
func TestGitSkillVerifiesMaterializedName(t *testing.T) {
	f := setup(t)
	src := filepath.Join(f.root, "api-source")
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".gitattributes"), []byte("SKILL.md filter=rename-skill\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.git(src, "add", "SKILL.md", ".gitattributes")
	f.git(src, "commit", "-m", "skill with checkout filter")
	f.git(src, "push", filepath.Join(f.remotes, "api.git"), "develop")
	t.Setenv("GIT_CONFIG_COUNT", "3")
	t.Setenv("GIT_CONFIG_KEY_2", "filter.rename-skill.smudge")
	t.Setenv("GIT_CONFIG_VALUE_2", "sed s/demo/other/")
	f.call(0, "skill-config", "add", "--git-url", "https://example.test/api.git")
	f.workspace([]Ref{}, "")
	f.call(0, "workbox-config", "add-skill", "analysis", "demo")
	root := filepath.Join(f.root, "instance")
	r := f.call(2, "create", "--path", root, "--config", "analysis")
	if object(r["error"])["message"] != "Registered Skill demo materialized as other" {
		t.Fatal(r)
	}
	assertAbsent(t, root)
}
func TestReadAndSearchInstance(t *testing.T) {
	f := setup(t)
	sha := strings.Repeat("1", 40)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-personal" {
			t.Error("missing token")
		}
		switch {
		case strings.Contains(r.URL.Path, "/two/"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/search"):
			if r.URL.Query().Get("page") == "2" {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"path":"f","ref":"release","startline":1,"data":"test"}]`)
		case strings.Contains(r.URL.Path, "/commits/"):
			fmt.Fprintf(w, `{"id":"%s"}`, sha)
		case strings.HasSuffix(r.URL.Path, "/raw"):
			if r.URL.Query().Get("ref") != sha {
				t.Error("read not pinned")
			}
			fmt.Fprint(w, "first\nsecond\n")
		default:
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	f.app.gitlabTransport = server.Client().Transport
	host := strings.TrimPrefix(server.URL, "https://")
	f.call(0, "init", "--backend", "sqlite", "--skip-skill")
	f.call(0, "auth", "gitlab", "set", "--host", host, "--token-file", f.file("token", "test-personal"))
	root := filepath.Join(f.root, "instance")
	if err := os.MkdirAll(filepath.Join(root, ".workbox"), 0755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal([]workspaceRepo{{Name: "one", URL: "git@ssh.example:group/one.git", DefaultBranch: "main"}, {Name: "two", URL: server.URL + "/group/two.git", DefaultBranch: "main"}})
	if err := os.WriteFile(filepath.Join(root, ".workbox/repos.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	f.call(2, "read", "--path", root, "--repo", "one", "--file", "f")
	f.call(0, "auth", "gitlab", "alias", "set", "--ssh-host", "ssh.example", "--host", host)
	for _, source := range []string{"git@ssh.example:group/one.git", "ssh://git@ssh.example/group/one.git", server.URL + "/group/one.git"} {
		p, e := f.app.gitlabProject(source)
		if e != nil || p.host != host || p.api != "/projects/group%2Fone" {
			t.Fatal(p, e)
		}
	}
	read := object(f.call(0, "read", "--path", root, "--repo", "one", "--file", "f", "--lines", "2-2")["data"])
	if read["data"] != "second\n" || read["commit"] != sha {
		t.Fatal(read)
	}
	partial := object(f.call(1, "search", "--path", root, "--repo", "one", "--repo", "two", "--ref", "one=release", "--query", "test")["data"])
	rows := array(partial["repos"])
	if len(rows) != 2 || len(array(object(rows[0])["hits"])) != 1 || object(rows[1])["coverage"] != "uncovered" {
		t.Fatal(rows)
	}
	f.call(2, "read", "--path", root, "--repo", "other", "--file", "f")
}
func TestPublishedCreateCancellation(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	for _, published := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		// Trigger at the exclusive publication operation so the CLI observes a cancelled context.
		f.app.publishInstance = func(parent *os.Root, stage, name string) error {
			cancel()
			if !published {
				return context.Canceled
			}
			return publishDir(parent, stage, name)
		}
		root := filepath.Join(f.root, fmt.Sprintf("cancel-%t", published))
		var out, diag strings.Builder
		code := f.app.run(ctx, []string{"create", "--config", "analysis", "--path", root, "--format", "json"}, &out, &diag, "test")
		var result map[string]any
		if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
			t.Fatal(err)
		}
		if published {
			if code != 0 || result["ok"] != true || !realDirectory(root) {
				t.Fatal(code, result)
			}
		} else {
			if code != 130 {
				t.Fatal(code, result)
			}
			assertAbsent(t, root)
		}
		cancel()
	}
}

func TestConcurrentCreatePublishesOneCompleteInstance(t *testing.T) {
	f := setup(t)
	f.workspace([]Ref{}, "")
	target := filepath.Join(f.root, "instance")
	ready := make(chan struct{}, 2)
	publish := make(chan struct{})
	f.app.publishInstance = func(parent *os.Root, stage, name string) error {
		ready <- struct{}{}
		<-publish
		return publishDir(parent, stage, name)
	}
	done := make(chan *Failure, 2)
	for i := 0; i < 2; i++ {
		go func() { _, e := f.app.prepare(context.Background(), "analysis", target, true); done <- e }()
	}
	<-ready
	<-ready
	assertAbsent(t, target)
	close(publish)
	winners := 0
	for i := 0; i < 2; i++ {
		e := <-done
		if e == nil {
			winners++
		} else if e.Code != "TARGET_EXISTS" {
			t.Fatal(e)
		}
	}
	if winners != 1 {
		t.Fatal("expected one publisher", winners)
	}
	raw, err := os.ReadFile(filepath.Join(target, ".workbox/repos.json"))
	if err != nil || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatal(err)
	}
	stages, _ := filepath.Glob(filepath.Join(f.root, ".workbox-create-*"))
	if len(stages) > 0 {
		t.Fatal(stages)
	}
}

func TestSSHAliasInExistingInstance(t *testing.T) {
	f := setup(t)
	f.call(0, "init", "--backend", "sqlite", "--skip-skill")
	for _, host := range []string{"GL.example", "gl.example:8443"} {
		f.call(0, "auth", "gitlab", "set", "--host", host, "--token-file", f.file("token", "private"))
	}
	_, e := f.app.gitlabProject("ssh://git@gl.example:2022/g/sub/p.git")
	if e == nil {
		t.Fatal("ambiguous HTTPS instance")
	}
	f.call(0, "auth", "gitlab", "alias", "set", "--ssh-host", "gl.example:2022", "--host", "gl.example:8443")
	p, e := f.app.gitlabProject("ssh://git@gl.example:2022/g/sub/p.git")
	if e != nil || p.host != "gl.example:8443" || p.api != "/projects/g%2Fsub%2Fp" {
		t.Fatal(p, e)
	}
}

func TestPhase2PortableSources(t *testing.T) {
	for _, factory := range storeFactories() {
		t.Run(factory.name, func(t *testing.T) {
			f := setup(t)
			f.app.store = factory.create(t)
			docs := `[{"project_url":"https://gitlab.example/team/docs","source_path":"rules.md","target_path":"AGENTS.md","ref":{"type":"branch","value":"release/v2"}}]`
			f.call(0, "skill-config", "add", "--installer", "npx-skills", "https://github.com/mattpocock/skills", "--skill", "grill-me")
			f.call(0, "workbox-config", "add", "example", "--docs-file", f.file("docs.json", docs), "--init-script-file", f.file("script.sh", "#!/bin/sh\necho example"))
			f.call(0, "workbox-config", "add-skill", "example", "grill-me")
			expected := object(object(f.call(0, "workbox-config", "show", "example")["data"])["workspace_config"])
			pkg := filepath.Join(f.root, "portable.json")
			f.call(0, "config", "export", "--output", pkg)
			f.app.store = &sqliteStore{path: filepath.Join(f.root, "receiver", "config.db")}
			f.call(0, "config", "import", "--input", pkg)
			actual := object(object(f.call(0, "workbox-config", "show", "example")["data"])["workspace_config"])
			delete(actual, "id")
			delete(expected, "id")
			if !jsonEqual(expected, actual) {
				t.Fatal(actual, expected)
			}
			// A conflict in the registration must not recreate the missing Workbox.
			f.call(0, "workbox-config", "remove", "example")
			f.call(0, "skill-config", "update", "grill-me", "--installer", "npx-skills", "https://gitlab.example/other/skills", "--skill", "grill-me")
			f.call(2, "config", "import", "--input", pkg)
			f.call(2, "workbox-config", "show", "example")
		})
	}
}

func TestAmbiguousSkillImportIsAtomic(t *testing.T) {
	f := setup(t)
	valid := `{"kind":"git","name":"old","url":"https://example.test/source"}`
	f.call(0, "skill-config", "add", "--installer", "npx-skills", "https://example.test/existing", "--skill", "existing")
	for _, extra := range []string{`,"URL":"https://example.test/other"`, `,"subdir":null`, `,"url":"https://example.test/other"`} {
		bad := strings.TrimSuffix(strings.Replace(valid, "old", "bad", 1), "}") + extra + "}"
		pkg := `{"schema_version":2,"repo_configs":[],"workspace_configs":[],"skill_configs":[` + valid + "," + bad + "]}"
		f.call(2, "config", "import", "--input", f.file("bad.json", pkg))
		rows := array(object(f.call(0, "skill-config", "list")["data"])["skill_configs"])
		if len(rows) != 1 || object(rows[0])["name"] != "existing" {
			t.Fatal(rows)
		}
	}
}

// CI enables the real, version-pinned npm installer. Git reads the local bare
// fixture via insteadOf. A regular offline go test never needs the npm registry.
func TestRealNPXRetrySameInstance(t *testing.T) {
	if os.Getenv("WORKBOX_TEST_REAL_NPX") != "1" {
		t.Skip("set WORKBOX_TEST_REAL_NPX=1 with Node 22.20+ and npx")
	}
	f := setup(t)
	src := filepath.Join(f.root, "api-source")
	dir := filepath.Join(src, "skills", "demo")
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range map[string]string{"SKILL.md": "---\nname: demo\ndescription: Retry fixture\n---\nTest.\n", "resources/check": "ok"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	f.git(src, "add", "skills")
	f.git(src, "commit", "-m", "test skill")
	f.git(src, "push", filepath.Join(f.remotes, "api.git"), "develop")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	userSkills := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(userSkills, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userSkills, "keep"), []byte("personal"), 0600); err != nil {
		t.Fatal(err)
	}
	before := skillTree(t, home)
	f.call(0, "skill-config", "add", "--installer", "npx-skills", "https://example.test/api.git", "--skill", "demo")
	f.workspace([]Ref{}, "")
	f.call(0, "workbox-config", "add-skill", "analysis", "demo")
	target := filepath.Join(f.root, "demand")
	create := []string{"create", "--path", target, "--config", "analysis"}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	response := f.call(1, create...)
	if errorCode(response) != "SKILL_INSTALL_FAILED" {
		t.Fatal(response)
	}
	assertAbsent(t, target)
	t.Setenv("PATH", originalPath)
	f.call(0, create...)
	installed := filepath.Join(target, ".agents", "skills", "demo")
	for _, rel := range []string{"SKILL.md", "resources/check"} {
		got, err := os.ReadFile(filepath.Join(installed, rel))
		want, _ := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || string(got) != string(want) {
			t.Fatalf("unexpected installed file %s: %v", rel, err)
		}
	}
	if !reflect.DeepEqual(before, skillTree(t, home)) {
		t.Fatal("npx changed caller home")
	}
	assertAbsent(t, filepath.Join(target, "skills-lock.json"))
}
