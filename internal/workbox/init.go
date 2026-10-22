package workbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type initOptions struct {
	backend, dsnFile, skillPath string
	install, skip, copy, noCopy bool
}

func initFlags(c command) (initOptions, *Failure) {
	f := c.flags
	o := initOptions{backend: f["backend"], dsnFile: f["mysql-dsn-file"], skillPath: f["skill-path"], install: f["install-skill"] == "true", skip: f["skip-skill"] == "true", copy: f["copy-config"] == "true", noCopy: f["no-copy-config"] == "true"}
	bad := func() (initOptions, *Failure) {
		return o, fail("INVALID_ARGUMENTS", "init requires --backend sqlite|mysql and exactly one of --install-skill or --skip-skill; check conflicting or inapplicable flags")
	}
	if (o.backend != "sqlite" && o.backend != "mysql") || o.install == o.skip || (o.copy && o.noCopy) {
		return bad()
	}
	if _, ok := f["mysql-dsn-file"]; ok && (o.backend != "mysql" || strings.TrimSpace(o.dsnFile) == "") {
		return bad()
	}
	if _, ok := f["skill-path"]; ok && (!o.install || strings.TrimSpace(o.skillPath) == "") {
		return bad()
	}
	if o.backend == "mysql" && os.Getenv("WORKBOX_MYSQL_DSN") != "" && os.Getenv("WORKBOX_MYSQL_DSN_FILE") != "" {
		return o, fail("INVALID_ARGUMENTS", "Set only one environment MySQL connection source")
	}
	if o.dsnFile != "" && (os.Getenv("WORKBOX_MYSQL_DSN") != "" || os.Getenv("WORKBOX_MYSQL_DSN_FILE") != "") {
		return o, fail("INVALID_ARGUMENTS", "--mysql-dsn-file cannot be combined with environment DSN sources")
	}
	return o, nil
}
func candidateStore(root string, old settingsRecord, o initOptions) (configStore, *Failure) {
	if o.backend == "sqlite" {
		return &sqliteStore{path: filepath.Join(root, "config.db")}, nil
	}
	if o.dsnFile != "" {
		p, e := absolute(o.dsnFile)
		if e != nil {
			return nil, e
		}
		raw, e := checkedFile(p, false)
		if e != nil {
			return nil, e
		}
		return newMySQLStore(strings.TrimSpace(string(raw)))
	}
	if os.Getenv("WORKBOX_MYSQL_DSN") != "" || os.Getenv("WORKBOX_MYSQL_DSN_FILE") != "" {
		return configuredMySQLStore()
	}
	if old.value != nil && old.value.Storage.Backend == "mysql" {
		return savedStore(root, old.value, false)
	}
	return nil, fail("INVALID_ARGUMENTS", "MySQL init requires --mysql-dsn-file, environment credentials, or saved MySQL credentials")
}
func sourceStore(root string, old settingsRecord) (configStore, *Failure) {
	if old.value != nil {
		return savedStore(root, old.value, true)
	}
	switch os.Getenv("WORKBOX_DB") {
	case "", "sqlite":
		return &sqliteStore{path: filepath.Join(root, "config.db")}, nil
	case "mysql":
		return configuredMySQLStore()
	default:
		return nil, invalid("WORKBOX_DB must be sqlite or mysql")
	}
}
func storeIdentity(s configStore) map[string]any {
	switch v := s.(type) {
	case *sqliteStore:
		return map[string]any{"backend": "sqlite", "path": v.path}
	case *mysqlStore:
		c := v.connector
		return map[string]any{"backend": "mysql", "network": c.Net, "address": c.Addr, "database": c.DBName, "user": c.User, "tls": c.TLSConfig != "" && c.TLSConfig != "false"}
	}
	return nil
}
func sameStore(a, b configStore) bool {
	switch x := a.(type) {
	case *sqliteStore:
		y, ok := b.(*sqliteStore)
		if !ok {
			return false
		}
		p, e := canonicalPath(x.path)
		q, f := canonicalPath(y.path)
		return e == nil && f == nil && p == q
	case *mysqlStore:
		y, ok := b.(*mysqlStore)
		return ok && x.connector.Net == y.connector.Net && x.connector.Addr == y.connector.Addr && x.connector.DBName == y.connector.DBName
	}
	return false
}
func sameConnection(a, b configStore) bool {
	if !sameStore(a, b) {
		return false
	}
	x, ok := a.(*mysqlStore)
	if !ok {
		return true
	}
	return x.connector.FormatDSN() == b.(*mysqlStore).connector.FormatDSN()
}
func overrideNames() []string {
	names := []string{}
	for _, k := range []string{"WORKBOX_DB", "WORKBOX_MYSQL_DSN", "WORKBOX_MYSQL_DSN_FILE"} {
		if os.Getenv(k) != "" {
			names = append(names, k)
		}
	}
	return names
}
func checkInitEnvironment(target configStore) *Failure {
	var actual configStore = target
	backend := os.Getenv("WORKBOX_DB")
	if backend != "" && backend != storeIdentity(target)["backend"] {
		e := fail("CONFIG_CONFLICT", "Environment overrides the chosen backend; unset WORKBOX_DB and run init again")
		e.Details["variables"] = overrideNames()
		return e
	}
	if _, ok := target.(*mysqlStore); ok && (os.Getenv("WORKBOX_MYSQL_DSN") != "" || os.Getenv("WORKBOX_MYSQL_DSN_FILE") != "") {
		s, e := configuredMySQLStore()
		if e != nil {
			return e
		}
		actual = s
	}
	if !sameConnection(target, actual) {
		e := fail("CONFIG_CONFLICT", "Environment overrides the chosen connection; unset MySQL connection variables and run init again")
		e.Details["variables"] = overrideNames()
		return e
	}
	return nil
}
func skillParent(old settingsRecord, raw string) (string, *Failure) {
	if raw == "" && old.value != nil && old.value.Skill.Parent != nil {
		raw = *old.value.Skill.Parent
	}
	if raw == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ioFailure("Could not locate user home")
		}
		raw = filepath.Join(home, ".agents", "skills")
	}
	return absolute(raw)
}

type initPlan struct {
	root                  string
	old                   settingsRecord
	options               initOptions
	target                configStore
	source                configStore
	frozen                *snapshot
	copyState, copyReason string
	frozenAt              string
	parent                string
}

func planInit(ctx context.Context, root string, old settingsRecord, o initOptions, target configStore) (*initPlan, *Failure) {
	p := &initPlan{root: root, old: old, options: o, target: target, copyState: "not_attempted"}
	if e := checkInitEnvironment(target); e != nil {
		return p, e
	}
	if _, e := target.snapshot(ctx); e != nil {
		return p, e
	}
	source, sourceErr := sourceStore(root, old)
	p.source = source
	same := sourceErr == nil && sameStore(source, target)
	// A changed credential can identify the same database without proving the
	// old connection is usable. Require a readable source before adopting it.
	if same && !sameConnection(source, target) {
		if _, e := source.snapshot(ctx); e != nil {
			sourceErr = e
			same = false
		}
	}

	if same {
		p.copyReason = "same_store"
		p.copyState = "skipped"
		if o.copy {
			p.copyState = "unchanged"
		}
	}
	if !same && o.noCopy {
		p.copyState = "skipped"
		p.copyReason = "user_selected_no_copy"
	} else if !same {
		if sourceErr != nil {
			if !o.copy {
				return p, fail("INVALID_ARGUMENTS", "Source is unreadable; explicitly choose --no-copy-config or repair the source")
			}
			return p, sourceErr
		}
		s, e := source.snapshot(ctx)
		if e != nil {
			if !o.copy {
				return p, fail("INVALID_ARGUMENTS", "Source is unreadable; explicitly choose --no-copy-config or repair the source")
			}
			return p, e
		}
		p.frozen = &s
		p.frozenAt = time.Now().UTC().Format(time.RFC3339)
		if len(s.Repos) == 0 && len(s.Workspaces) == 0 {
			p.copyState = "skipped"
			p.copyReason = "source_empty"
			if o.copy {
				p.copyState = "unchanged"
			}
		} else if !o.copy {
			return p, fail("INVALID_ARGUMENTS", "Switching from a nonempty source requires --copy-config or --no-copy-config")
		}
	}
	if o.install {
		parent, e := skillParent(old, o.skillPath)
		if e != nil {
			return p, e
		}
		p.parent = parent
	}
	return p, nil
}
func initResult(root string, old settingsRecord) map[string]any {
	var rev any
	if old.value != nil {
		rev = old.value.Revision
	}
	return map[string]any{"state": "failed", "initialized": old.exists, "settings": map[string]any{"path": filepath.Join(root, "settings.json"), "revision": nil, "publication": "not_published", "active_revision": rev}, "storage": map[string]any{"backend": nil, "target": nil, "preparation": "not_attempted"}, "copy": map[string]any{"state": "not_attempted", "added": nil, "unchanged": nil}, "skill": map[string]any{"state": "not_attempted"}, "effective_storage": nil}
}
func refreshInitResult(root string, result map[string]any) {
	r, e := loadSettings(root)
	if e != nil {
		result["initialized"] = nil
		result["settings"].(map[string]any)["active_revision"] = nil
		return
	}
	result["initialized"] = r.exists
	if r.value != nil {
		result["settings"].(map[string]any)["active_revision"] = r.value.Revision
		s, e := savedStore(root, r.value, true)
		if e == nil {
			result["effective_storage"] = map[string]any{"target": storeIdentity(s), "backend": storeIdentity(s)["backend"], "source": "settings.json with environment overrides", "overrides": overrideNames()}
		}
	}
}
func (a *app) executeInit(ctx context.Context, p *initPlan, result map[string]any) (any, *Failure) {
	settings := result["settings"].(map[string]any)
	storage := result["storage"].(map[string]any)
	copyResult := result["copy"].(map[string]any)
	skill := result["skill"].(map[string]any)
	failure := func(e *Failure) (any, *Failure) { refreshInitResult(p.root, result); e.Data = result; return nil, e }
	unlock, e := acquireInitLock(ctx, p.root)
	if e != nil {
		return failure(e)
	}
	defer unlock()
	if e = settingsUnchanged(p.root, p.old); e != nil {
		return failure(e)
	}
	revision, e := uuid()
	if e != nil {
		return failure(e)
	}
	settings["revision"] = revision
	value := localSettings{Version: 1, Revision: revision, InitializedAt: time.Now().UTC().Format(time.RFC3339), Storage: storageSettings{Backend: p.options.backend}, Skill: skillPreference{Install: p.options.install}}
	if p.options.install {
		value.Skill.Parent = &p.parent
		skill["parent_dir"] = p.parent
		skill["path"] = filepath.Join(p.parent, "workbox")
	}
	if ctx.Err() != nil {
		return failure(fail("INTERRUPTED", "Initialization interrupted"))
	}
	if mysql, ok := p.target.(*mysqlStore); ok {
		dir := filepath.Join(p.root, "credentials")
		if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return failure(ioFailure("Could not create credentials directory"))
		}
		if e := privateDir(dir); e != nil {
			return failure(e)
		}
		value.Storage.DSNFile = filepath.Join(dir, "mysql-"+revision+".dsn")
		visible, e := atomicPrivateFile(value.Storage.DSNFile, []byte(mysql.connector.FormatDSN()+"\n"), false)
		if visible {
			result["state"] = "partial"
		}
		if e != nil {
			return failure(e)
		}
	}
	// Re-read the target after taking the init lock. Preparation is an ordinary
	// CAS transaction with no fabricated business records.
	current, e := p.target.snapshot(ctx)
	if e != nil {
		storage["preparation"] = "failed"
		return failure(e)
	}
	result["state"] = "partial" // A failed preparation may already have created schema.
	if e = p.target.commit(ctx, current, current); e != nil {
		storage["preparation"] = "failed"
		if e.Code == "COMMIT_OUTCOME_UNKNOWN" {
			storage["preparation"] = "unknown"
		}
		return failure(e)
	}
	storage["preparation"] = "ready"
	if p.copyState == "not_attempted" && p.frozen != nil {
		copyResult["state"] = "failed"
		portable, e := p.frozen.portable()
		if e != nil {
			return failure(e)
		}
		raw, err := json.Marshal(portable)
		if err != nil {
			return failure(ioFailure("Could not prepare configuration copy"))
		}
		incoming, e := a.parsePackage(ctx, raw)
		if e != nil {
			return failure(e)
		}
		targetApp := &app{store: p.target, gitRunner: a.gitRunner}
		data, e := targetApp.importPackage(ctx, incoming)
		if e != nil {
			if e.Code == "COMMIT_OUTCOME_UNKNOWN" {
				copyResult["state"] = "unknown"
			}
			return failure(e)
		}
		imported := data.(map[string]any)
		count := 0
		for _, kind := range []string{"added", "unchanged"} {
			values := imported[kind].(map[string][]string)
			counts := map[string]int{"repo_configs": len(values["repo_configs"]), "workspace_configs": len(values["workspace_configs"])}
			copyResult[kind] = counts
			if kind == "added" {
				count = counts["repo_configs"] + counts["workspace_configs"]
			}
		}
		copyResult["state"] = "copied"
		if count == 0 {
			copyResult["state"] = "unchanged"
			copyResult["reason"] = "identical_content"
		}
	} else {
		copyResult["state"] = p.copyState
		copyResult["reason"] = p.copyReason
	}
	if ctx.Err() != nil {
		return failure(fail("INTERRUPTED", "Initialization interrupted"))
	}
	if e = settingsUnchanged(p.root, p.old); e != nil {
		return failure(e)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return failure(ioFailure("Could not encode settings"))
	}
	visible, e := atomicPrivateFile(filepath.Join(p.root, "settings.json"), append(raw, '\n'), true)
	if e != nil {
		if visible {
			settings["publication"] = "unknown"
		}
		return failure(e)
	}
	settings["publication"] = "published"
	refreshInitResult(p.root, result)
	if ctx.Err() != nil {
		return failure(fail("INTERRUPTED", "Initialization interrupted"))
	}
	if p.options.install {
		data, e := installSkill(ctx, map[string]string{"path": p.parent})
		if e != nil {
			skill["state"] = "partial"
			if e.Code == "SKILL_CONFLICT" {
				skill["state"] = "failed"
			}
			e.Details["next_step"] = "workbox skill install --path " + p.parent
			return failure(e)
		}
		skill["state"] = data.(map[string]any)["state"]
	} else {
		skill["state"] = "skipped"
	}
	result["state"] = "initialized"
	result["next_step"] = "workbox repo-config list"
	return result, nil
}
func (a *app) initialize(ctx context.Context, c command, format string, stderr io.Writer) (data any, failure *Failure) {
	interactive := len(c.flags) == 0 && format == "text" && terminalInteractive(stderr)
	if !interactive {
		if _, e := initFlags(c); e != nil {
			return nil, e
		}
	}
	root, e := settingsRoot()
	if e != nil {
		return nil, e
	}
	old, e := loadSettings(root)
	if e != nil {
		return nil, e
	}
	result := initResult(root, old)
	defer func() {
		if failure != nil {
			refreshInitResult(root, result)
			failure.Data = result
		}
	}()
	var o initOptions
	var target configStore
	var p *initPlan
	if interactive {
		p, e = a.interactiveInit(ctx, root, old, stderr)
	} else {
		o, e = initFlags(c)
		if e == nil {
			target, e = candidateStore(root, old, o)
		}
		if e == nil {
			p, e = planInit(ctx, root, old, o, target)
		}
	}
	if p != nil && p.target != nil {
		result["storage"].(map[string]any)["backend"] = p.options.backend
		result["storage"].(map[string]any)["target"] = storeIdentity(p.target)
	}
	if e != nil {
		return nil, e
	}
	return a.executeInit(ctx, p, result)
}

func initSummary(w io.Writer, p *initPlan) {
	fmt.Fprintln(w, "\nInitialization plan (no changes have been made):")
	human(w, map[string]any{"source": storeIdentity(p.source), "target": storeIdentity(p.target), "settings": filepath.Join(p.root, "settings.json"), "copy_config": p.options.copy, "install_skill": p.options.install, "skills_parent": p.parent}, 0)
	if p.frozen != nil {
		fmt.Fprintf(w, "Frozen source at %s: %d repositories, %d workspaces. Later source changes are not copied.\n", p.frozenAt, len(p.frozen.Repos), len(p.frozen.Workspaces))
	}
	if p.options.copy && p.copyState == "not_attempted" {
		fmt.Fprintln(w, "Copy validates Git remote branches; existing target configurations are retained, conflicts abort the entire copy.")
	}
	fmt.Fprintln(w, "Will prepare storage and publish settings, then install the selected Skill.")
}
