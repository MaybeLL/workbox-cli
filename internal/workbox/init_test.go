package workbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("WORKBOX_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("WORKBOX_STATE_HOME", filepath.Join(home, "state"))
	for _, name := range []string{"WORKBOX_DB", "WORKBOX_MYSQL_DSN", "WORKBOX_MYSQL_DSN_FILE"} {
		t.Setenv(name, "")
	}
	return home
}
func initCall(t *testing.T, want int, args ...string) envelope {
	t.Helper()
	var out, err bytes.Buffer
	code := Run(context.Background(), append(args, "--format", "json"), &out, &err, "test")
	if code != want {
		t.Fatalf("%v: exit %d want %d: %s %s", args, code, want, &out, &err)
	}
	var result envelope
	decoder := json.NewDecoder(&out)
	if e := decoder.Decode(&result); e != nil {
		t.Fatal(e)
	}
	if out.Len() != 0 {
		t.Fatal("more than one JSON envelope")
	}
	return result
}
func TestInitGateBeforeSideEffects(t *testing.T) {
	home := initHome(t)
	t.Setenv("WORKBOX_DB", "mysql")
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", filepath.Join(home, "missing-secret"))
	for _, args := range [][]string{{"repo-config", "list"}, {"workspace-config", "add", "empty"}, {"config", "import", "--input", filepath.Join(home, "missing")}, {"config", "export", "--output", filepath.Join(home, "out")}, {"workspace", "create", "--config", "absent", "--path", filepath.Join(home, "workspace")}, {"workspace", "plan", "--config", "absent", "--path", filepath.Join(home, "workspace")}} {
		r := initCall(t, 2, args...)
		if r.Error.Code != "NOT_INITIALIZED" {
			t.Fatal(r.Error)
		}
	}
	assertAbsent(t, filepath.Join(home, "config"))
	assertAbsent(t, filepath.Join(home, "workspace"))
	assertAbsent(t, filepath.Join(home, "out"))
	initCall(t, 0, "--help")
	initCall(t, 0, "--version")
	initCall(t, 0, "workspace", "status", "--path", home)
	r := initCall(t, 2, "repo-config", "add", "x")
	if r.Error.Code != "INVALID_ARGUMENTS" {
		t.Fatal(r.Error)
	}
}
func TestInitSQLiteAdoptsLegacyAndReinitializes(t *testing.T) {
	f := setup(t)
	f.call(0, "workspace-config", "add", "legacy")
	before := storeRead(t, f.sqlite)
	first := initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill").Data.(map[string]any)
	if first["initialized"] != true || first["state"] != "initialized" {
		t.Fatal(first)
	}
	if !sameSnapshot(before, storeRead(t, f.sqlite)) {
		t.Fatal("legacy IDs or data changed")
	}
	initCall(t, 0, "workspace-config", "show", "legacy")
	root, e := settingsRoot()
	if e != nil {
		t.Fatal(e)
	}
	old, e := loadSettings(root)
	if e != nil {
		t.Fatal(e)
	}
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill", "--copy-config")
	now, e := loadSettings(root)
	if e != nil || old.value.Revision == now.value.Revision {
		t.Fatal("revision did not change", e)
	}
	if !sameSnapshot(before, storeRead(t, f.sqlite)) {
		t.Fatal("same-store init copied data")
	}
}
func TestInitStrictSettings(t *testing.T) {
	mutations := map[string]func(string) string{
		"duplicate": func(s string) string {
			return strings.Replace(s, `"settings_version": 1`, `"settings_version": 1, "settings_version": 1`, 1)
		},
		"unknown": func(s string) string {
			return strings.Replace(s, `"settings_version": 1`, `"settings_version": 1, "secret": "bad"`, 1)
		},
		"missing_boolean": func(s string) string { return strings.Replace(s, `"install": false,`, "", 1) },
		"null_boolean":    func(s string) string { return strings.Replace(s, `"install": false`, `"install": null`, 1) },
		"wrong_type": func(s string) string {
			return strings.Replace(s, `"settings_version": 1`, `"settings_version": "1"`, 1)
		},
		"future": func(s string) string { return strings.Replace(s, `"settings_version": 1`, `"settings_version": 99`, 1) },
		"broken": func(s string) string { return "{" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			home := initHome(t)
			initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
			path := filepath.Join(home, "config", "settings.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(mutate(string(raw))), 0600); err != nil {
				t.Fatal(err)
			}
			r := initCall(t, 2, "repo-config", "list")
			want := "CONFIG_INVALID"
			if name == "future" {
				want = "SCHEMA_UNSUPPORTED"
			}
			if r.Error.Code != want {
				t.Fatal(r.Error)
			}
			initCall(t, 2, "init", "--backend", "sqlite", "--skip-skill")
			initCall(t, 0, "--help")
		})
	}
}
func TestInitSettingsPermissionsAndSymlinks(t *testing.T) {
	home := initHome(t)
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	path := filepath.Join(home, "config", "settings.json")
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	initCall(t, 0, "repo-config", "list")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	r := initCall(t, 2, "repo-config", "list")
	if r.Error.Code != "CONFIG_INVALID" {
		t.Fatal(r.Error)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".backup"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".backup", path); err != nil {
		t.Fatal(err)
	}
	r = initCall(t, 2, "repo-config", "list")
	if r.Error.Code != "CONFIG_INVALID" {
		t.Fatal(r.Error)
	}
}
func TestInitArgumentsAndEnvironmentHaveNoWrites(t *testing.T) {
	home := initHome(t)
	for _, args := range [][]string{{"init"}, {"init", "--backend", "sqlite"}, {"init", "--backend", "sqlite", "--skip-skill", "--install-skill"}, {"init", "--backend", "sqlite", "--skip-skill=false"}, {"init", "--backend", "sqlite", "--skip-skill", "--mysql-dsn-file", "missing"}, {"init", "--backend", "sqlite", "--skip-skill", "--skill-path", "x"}, {"init", "--backend", "sqlite", "--skip-skill", "--copy-config", "--no-copy-config"}} {
		r := initCall(t, 2, args...)
		if r.Error.Code != "INVALID_ARGUMENTS" {
			t.Fatal(r.Error)
		}
	}
	t.Setenv("WORKBOX_DB", "mysql")
	r := initCall(t, 2, "init", "--backend", "sqlite", "--skip-skill")
	if r.Error.Code != "CONFIG_CONFLICT" {
		t.Fatal(r.Error)
	}
	assertAbsent(t, filepath.Join(home, "config"))
}
func TestInitSkillFailureKeepsSettingsUsable(t *testing.T) {
	home := initHome(t)
	parent := filepath.Join(home, "skills")
	if err := os.MkdirAll(filepath.Join(parent, "workbox"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "workbox", "user-file"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r := initCall(t, 2, "init", "--backend", "sqlite", "--install-skill", "--skill-path", parent)
	data := r.Data.(map[string]any)
	if r.Error.Code != "SKILL_CONFLICT" || data["state"] != "partial" || data["initialized"] != true {
		t.Fatal(r)
	}
	initCall(t, 0, "workspace-config", "add", "works")
	initCall(t, 0, "init", "--backend", "sqlite", "--install-skill", "--skill-path", filepath.Join(home, "other-skills"))
	initCall(t, 0, "skill", "install", "--path", filepath.Join(home, "other-skills"))
}
func TestInitStalePlanAndLockCancellation(t *testing.T) {
	initHome(t)
	root, e := settingsRoot()
	if e != nil {
		t.Fatal(e)
	}
	old, e := loadSettings(root)
	if e != nil {
		t.Fatal(e)
	}
	o := initOptions{backend: "sqlite", skip: true}
	target, _ := candidateStore(root, old, o)
	p, e := planInit(context.Background(), root, old, o, target)
	if e != nil {
		t.Fatal(e)
	}
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	_, e = (&app{}).executeInit(context.Background(), p, initResult(root, old))
	if e == nil || e.Code != "CONFIG_CONFLICT" {
		t.Fatal(e)
	}
	unlock, e := acquireInitLock(context.Background(), root)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = acquireInitLock(ctx, root)
	if e == nil || e.Code != "INTERRUPTED" {
		t.Fatal(e)
	}
	// Ordinary commands do not take the initialization lock.
	initCall(t, 0, "repo-config", "list")
}
func TestInitNoCopyCanRepairUnreadableSource(t *testing.T) {
	home := initHome(t)
	root := filepath.Join(home, "config")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	rev := "11111111-1111-4111-8111-111111111111"
	saved := localSettings{Version: 1, Revision: rev, InitializedAt: "2026-10-22T04:00:00Z", Storage: storageSettings{Backend: "mysql", DSNFile: filepath.Join(root, "credentials", "mysql-"+rev+".dsn")}}
	raw, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	r := initCall(t, 2, "init", "--backend", "sqlite", "--skip-skill")
	if r.Error.Code != "INVALID_ARGUMENTS" {
		t.Fatal(r.Error)
	}
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill", "--no-copy-config")
	initCall(t, 0, "repo-config", "list")
}
func TestInitInactiveCredentialsAndEnvGate(t *testing.T) {
	home := initHome(t)
	root := filepath.Join(home, "config")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	rev := "11111111-1111-4111-8111-111111111111"
	saved := localSettings{Version: 1, Revision: rev, InitializedAt: "2026-10-22T04:00:00Z", Storage: storageSettings{Backend: "mysql", DSNFile: filepath.Join(root, "credentials", "mysql-"+rev+".dsn")}}
	raw, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBOX_DB", "sqlite")
	initCall(t, 0, "repo-config", "list")
	t.Setenv("WORKBOX_DB", "mysql")
	t.Setenv("WORKBOX_MYSQL_DSN", "user:secret@tcp(127.0.0.1:1)/workbox")
	s, e := initializedStore()
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := s.(*mysqlStore); !ok {
		t.Fatal("override ignored")
	}
	if err := os.Remove(filepath.Join(root, "settings.json")); err != nil {
		t.Fatal(err)
	}
	r := initCall(t, 2, "repo-config", "list")
	if r.Error.Code != "NOT_INITIALIZED" {
		t.Fatal(r.Error)
	}
}
func TestInitPipeNeverPrompts(t *testing.T) {
	initHome(t)
	cmd := exec.Command(testBinary, "init")
	cmd.Stdin = strings.NewReader("1\ny\n")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "INVALID_ARGUMENTS") {
		t.Fatalf("unexpected pipe result: %s %v", out, err)
	}
}

type failedInitStore struct{ failure *Failure }

func (s failedInitStore) snapshot(context.Context) (snapshot, *Failure)       { return emptySnapshot(), nil }
func (s failedInitStore) commit(context.Context, snapshot, snapshot) *Failure { return s.failure }
func TestInitUnknownPreparationStopsPublication(t *testing.T) {
	home := initHome(t)
	root := filepath.Join(home, "config")
	old := settingsRecord{}
	s := failedInitStore{commitFailure(errors.New("lost acknowledgement"))}
	p := &initPlan{root: root, old: old, options: initOptions{backend: "sqlite", skip: true}, target: s, copyState: "skipped"}
	_, e := (&app{}).executeInit(context.Background(), p, initResult(root, old))
	if e == nil || e.Code != "COMMIT_OUTCOME_UNKNOWN" {
		t.Fatal(e)
	}
	data := e.Data.(map[string]any)
	if data["state"] != "partial" || data["storage"].(map[string]any)["preparation"] != "unknown" || data["copy"].(map[string]any)["state"] != "not_attempted" {
		t.Fatal(data)
	}
	assertAbsent(t, filepath.Join(root, "settings.json"))
}

func TestPrivatePublicationReportsDurabilityFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	visible, e := publishPrivateFile(path, []byte("new"), true, func(*os.File) error { return errors.New("sync failure") })
	if !visible || e == nil || e.Code != "IO_ERROR" {
		t.Fatal(visible, e)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "new" {
		t.Fatal("visible file was reverted", err)
	}
	visible, e = atomicPrivateFile(path, []byte("replace forbidden"), false)
	if visible || e == nil {
		t.Fatal("exclusive credential publication overwrote a file")
	}
}

func TestInitMySQLCopyAndCredentialPersistence(t *testing.T) {
	f := setup(t)
	f.repo("api")
	f.call(0, "team-config", "add", "credit", "--name", "授信")
	f.call(0, "repo-config", "add-team", "api", "--team", "credit")
	f.call(0, "workspace-config", "add", "analysis", "--repos-file", f.file("refs.json", `[{"repo":"api","target_dir":"api"}]`))
	before := storeRead(t, f.sqlite)
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	t.Setenv("WORKBOX_DB", "")
	mysql := mysqlTestStore(t).(*mysqlStore)
	input := f.file("mysql-input", mysql.connector.FormatDSN())
	r := initCall(t, 0, "init", "--backend", "mysql", "--mysql-dsn-file", input, "--skip-skill", "--copy-config")
	if r.Data.(map[string]any)["copy"].(map[string]any)["state"] != "copied" {
		t.Fatal(r.Data)
	}
	target := storeRead(t, mysql)
	if target.Teams["credit"].ID == before.Teams["credit"].ID || target.Repos["api"].TeamIDs[0] != target.Teams["credit"].ID {
		t.Fatal("team copy failed")
	}
	if target.Repos["api"].ID == before.Repos["api"].ID || len(target.Workspaces) != 1 || !sameSnapshot(before, storeRead(t, f.sqlite)) {
		t.Fatal("copy failed to remap IDs or modified source")
	}
	root, e := settingsRoot()
	if e != nil {
		t.Fatal(e)
	}
	old, e := loadSettings(root)
	if e != nil {
		t.Fatal(e)
	}
	info, err := os.Stat(old.value.Storage.DSNFile)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential mode", err)
	}
	raw, err := os.ReadFile(input)
	if err != nil || string(raw) != mysql.connector.FormatDSN() {
		t.Fatal("input was modified")
	}
	if err = os.WriteFile(input, []byte("not a connection"), 0600); err != nil {
		t.Fatal(err)
	}
	initCall(t, 0, "workspace-config", "show", "analysis")
	initCall(t, 0, "init", "--backend", "mysql", "--skip-skill", "--copy-config")
	now, e := loadSettings(root)
	if e != nil || now.value.Storage.DSNFile == old.value.Storage.DSNFile {
		t.Fatal("credential was not versioned", e)
	}
	if _, err = os.Stat(old.value.Storage.DSNFile); err != nil {
		t.Fatal("old credential removed")
	}
	mysql2 := mysqlTestStore(t).(*mysqlStore)
	input2 := f.file("mysql-input2", mysql2.connector.FormatDSN())
	initCall(t, 0, "init", "--backend", "mysql", "--mysql-dsn-file", input2, "--skip-skill", "--copy-config")
	if len(storeRead(t, mysql2).Workspaces) != 1 || !sameSnapshot(target, storeRead(t, mysql)) {
		t.Fatal("MySQL to MySQL copy failed")
	}
	r = initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill", "--copy-config")
	if r.Data.(map[string]any)["copy"].(map[string]any)["state"] != "unchanged" || !sameSnapshot(before, storeRead(t, f.sqlite)) {
		t.Fatal("reverse merge was not idempotent")
	}
}
func TestInitMySQLConflictPreservesSourceAndSettings(t *testing.T) {
	f := setup(t)
	f.repo("api")
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	before := storeRead(t, f.sqlite)
	mysql := mysqlTestStore(t).(*mysqlStore)
	different := before.clone()
	r := different.Repos["api"]
	r.Branch = "different"
	different.Repos["api"] = r
	if e := mysql.commit(context.Background(), emptySnapshot(), different); e != nil {
		t.Fatal(e)
	}
	root, _ := settingsRoot()
	old, _ := loadSettings(root)
	t.Setenv("WORKBOX_DB", "")
	result := initCall(t, 2, "init", "--backend", "mysql", "--mysql-dsn-file", f.file("dsn", mysql.connector.FormatDSN()), "--skip-skill", "--copy-config")
	if result.Error.Code != "CONFIG_CONFLICT" || result.Data.(map[string]any)["initialized"] != true {
		t.Fatal(result)
	}
	now, e := loadSettings(root)
	if e != nil || old.digest != now.digest || !sameSnapshot(different, storeRead(t, mysql)) || !sameSnapshot(before, storeRead(t, f.sqlite)) {
		t.Fatal("conflict altered settings or data", e)
	}
}
func TestInitUsesFrozenSourceAndRetainsCopiedDataOnSettingsFailure(t *testing.T) {
	for _, publishFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "frozen_source", true: "settings_failure"}[publishFailure], func(t *testing.T) {
			f := setup(t)
			f.repo("api")
			initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
			mysql := mysqlTestStore(t).(*mysqlStore)
			t.Setenv("WORKBOX_DB", "")
			root, _ := settingsRoot()
			old, _ := loadSettings(root)
			o := initOptions{backend: "mysql", skip: true, copy: true}
			plan, e := planInit(context.Background(), root, old, o, mysql)
			if e != nil {
				t.Fatal(e)
			}
			f.call(0, "workspace-config", "add", "late")
			a := &app{gitRunner: gitCLI{}}
			if publishFailure {
				a.gitRunner = hookedGit{gitCLI{}, func(args []string) *Failure {
					if args[0] == "ls-remote" {
						if err := os.Chmod(root, 0500); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}}
				t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			}
			_, e = a.executeInit(context.Background(), plan, initResult(root, old))
			if publishFailure {
				if e == nil || e.Code != "IO_ERROR" || e.Data.(map[string]any)["copy"].(map[string]any)["state"] != "copied" {
					t.Fatal(e)
				}
				now, _ := loadSettings(root)
				if now.digest != old.digest {
					t.Fatal("old settings changed")
				}
			} else if e != nil {
				t.Fatal(e)
			}
			target := storeRead(t, mysql)
			if len(target.Repos) != 1 || len(target.Workspaces) != 0 {
				t.Fatal("source snapshot changed or copied target was rolled back")
			}
		})
	}
}

type ackLostStore struct {
	configStore
	commits int
}

func (s *ackLostStore) commit(ctx context.Context, old, next snapshot) *Failure {
	s.commits++
	if e := s.configStore.commit(ctx, old, next); e != nil {
		return e
	}
	if s.commits == 2 {
		return commitFailure(errors.New("ack lost"))
	}
	return nil
}
func TestInitCopyUnknownRetainsTargetAndOldSettings(t *testing.T) {
	f := setup(t)
	f.repo("api")
	initCall(t, 0, "init", "--backend", "sqlite", "--skip-skill")
	root, _ := settingsRoot()
	old, _ := loadSettings(root)
	source := storeRead(t, f.sqlite)
	target := &sqliteStore{path: filepath.Join(f.root, "target", "config.db")}
	wrapped := &ackLostStore{configStore: target}
	p := &initPlan{root: root, old: old, options: initOptions{backend: "sqlite", skip: true, copy: true}, target: wrapped, frozen: &source, copyState: "not_attempted"}
	_, e := (&app{gitRunner: gitCLI{}}).executeInit(context.Background(), p, initResult(root, old))
	if e == nil || e.Code != "COMMIT_OUTCOME_UNKNOWN" {
		t.Fatal(e)
	}
	result := e.Data.(map[string]any)
	if result["copy"].(map[string]any)["state"] != "unknown" || result["storage"].(map[string]any)["preparation"] != "ready" || result["settings"].(map[string]any)["publication"] != "not_published" {
		t.Fatal(result)
	}
	now, _ := loadSettings(root)
	if now.digest != old.digest || len(storeRead(t, target).Repos) != 1 {
		t.Fatal("lost ack resulted in destructive rollback")
	}
}

func TestInitEnvironmentConnectionNormalization(t *testing.T) {
	home := initHome(t)
	dsn := "user:private-password@tcp(localhost:3306)/workbox"
	target, e := newMySQLStore(dsn)
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(home, "dsn")
	if err := os.WriteFile(file, []byte(dsn+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", file)
	if e := checkInitEnvironment(target); e != nil {
		t.Fatal("same connection in a different file conflicted", e)
	}
	t.Setenv("WORKBOX_MYSQL_DSN_FILE", "")
	t.Setenv("WORKBOX_MYSQL_DSN", "user:different-password@tcp(localhost:3306)/workbox")
	if e := checkInitEnvironment(target); e == nil || e.Code != "CONFIG_CONFLICT" || strings.Contains(e.Error(), "password") {
		t.Fatal(e)
	}
}
func TestInitCredentialSourcesRejectLeafLinks(t *testing.T) {
	home := initHome(t)
	file := filepath.Join(home, "input")
	if err := os.WriteFile(file, []byte("user:secret@tcp(localhost:3306)/workbox"), 0644); err != nil {
		t.Fatal(err)
	}
	link := file + "-link"
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	_, e := candidateStore(filepath.Join(home, "config"), settingsRecord{}, initOptions{backend: "mysql", dsnFile: link})
	if e == nil || e.Code != "CONFIG_INVALID" {
		t.Fatal(e)
	}
	if _, e = checkedFile(file, false); e != nil {
		t.Fatal("explicit input unnecessarily required private mode", e)
	}
	if _, e = checkedFile(file, true); e == nil || e.Code != "CONFIG_INVALID" {
		t.Fatal("managed input accepted broad permissions", e)
	}
	assertAbsent(t, filepath.Join(home, "config"))
}

func TestInitRepeatedFormatPreservesJSONError(t *testing.T) {
	initHome(t)
	r := initCall(t, 2, "init", "--backend", "sqlite", "--skip-skill", "--format", "text")
	if r.Error.Code != "INVALID_ARGUMENTS" {
		t.Fatal(r.Error)
	}
}
