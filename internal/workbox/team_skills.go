package workbox

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/cases"
	"gopkg.in/yaml.v3"
)

type SkillSource struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Subdir string `json:"subdir,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

func validateSkillSource(v SkillSource) *Failure {
	if e := validateName(v.Name); e != nil {
		return e
	}
	if v.Name == "workbox" {
		return invalid("Skill name workbox is reserved")
	}
	if e := validateURL(v.URL); e != nil {
		return e
	}
	switch v.Kind {
	case "npx-skills":
		if v.Subdir != "" || v.Branch != "" || v.Commit != "" {
			return invalid("Unexpected npx source options")
		}
	case "git":
		if v.Subdir != "" && !safeRelative(v.Subdir) {
			return invalid("Invalid Skill subdirectory")
		}
		if v.Branch != "" {
			if v.Commit != "" {
				return invalid("Choose branch or commit")
			}
			return validateBaselineRef(&BaselineRef{"branch", v.Branch})
		}
		if v.Commit != "" {
			return validateBaselineRef(&BaselineRef{"commit", v.Commit})
		}
	default:
		return invalid("Unknown Skill installer")
	}
	return nil
}

func decodeSkill(raw []byte) (SkillSource, *Failure) {
	var v SkillSource
	if e := checkJSON(raw); e != nil {
		return v, e
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return v, invalid("Invalid Skill source")
	}
	keys := []string{"name", "kind", "url"}
	for _, key := range []string{"subdir", "branch", "commit"} {
		if _, ok := fields[key]; ok {
			keys = append(keys, key)
		}
	}
	if e := exact(raw, &v, keys...); e != nil {
		return v, e
	}
	return v, validateSkillSource(v)
}

func validateSkillSelection(w Workspace) *Failure {
	seen := map[string]bool{}
	for _, name := range w.Skills {
		if validateName(name) != nil || name == "workbox" || seen[name] {
			return invalid("Invalid or duplicate selected Skill " + name)
		}
		seen[name] = true
	}
	if len(seen) > 0 {
		for _, ref := range w.Repos {
			if overlaps(cases.Fold().String(ref.Target), ".agents/skills") {
				return invalid("Repository target " + ref.Target + " overlaps .agents/skills")
			}
		}
	}
	return nil
}

func declaredSkillName(raw []byte) (string, *Failure) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	header, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", invalid("SKILL.md requires YAML front matter")
	}
	fields, _, ok := strings.Cut(header+"\n", "\n---\n")
	var meta struct {
		Name string `yaml:"name"`
	}
	if !ok || yaml.Unmarshal([]byte(fields), &meta) != nil || validateName(meta.Name) != nil {
		return "", invalid("Missing or invalid SKILL.md name")
	}
	if meta.Name == "workbox" {
		return "", invalid("Skill name workbox is reserved")
	}
	return meta.Name, nil
}

// Both operations inspect one fetched revision, including when its branch moves.
func (a *app) registerGitSkill(ctx context.Context, v SkillSource) (string, *Failure) {
	return a.withGitSkill(ctx, v, nil)
}
func (a *app) installGitSkill(ctx context.Context, v SkillSource, dest string) *Failure {
	_, e := a.withGitSkill(ctx, v, func(temp, name string) *Failure {
		if name != v.Name {
			return invalid("Registered Skill " + v.Name + " now declares " + name)
		}
		subtree := v.Subdir
		if subtree == "" {
			subtree = "."
		}
		if _, e := a.git(ctx, temp, "--literal-pathspecs", "checkout", "FETCH_HEAD", "--", subtree); e != nil {
			return e
		}
		return copySelectedSkill(filepath.Join(temp, filepath.FromSlash(subtree)), dest, v.Name)
	})
	return e
}
func (a *app) withGitSkill(ctx context.Context, v SkillSource, install func(string, string) *Failure) (string, *Failure) {
	temp, err := os.MkdirTemp("", "workbox-git-skill-")
	if err != nil {
		return "", ioFailure("Cannot stage Git Skill")
	}
	defer os.RemoveAll(temp)
	initArgs := []string{"init", "--quiet"}
	if len(v.Commit) == 64 {
		initArgs = append(initArgs, "--object-format=sha256")
	}
	if _, e := a.git(ctx, temp, initArgs...); e != nil {
		return "", e
	}
	ref := "HEAD"
	if v.Branch != "" {
		ref = "refs/heads/" + v.Branch
	}
	if v.Commit != "" {
		ref = v.Commit
	}
	// A promisor remote lets Git lazily fetch selected blobs on capable servers.
	if _, e := a.git(ctx, temp, "remote", "add", "origin", v.URL); e != nil {
		return "", e
	}
	if _, e := a.git(ctx, temp, "config", "remote.origin.promisor", "true"); e != nil {
		return "", e
	}
	if _, _, e := a.gitRunner.run(ctx, temp, 2*time.Minute, true, []int{0}, "fetch", "--filter=blob:none", "--depth=1", "--no-tags", "--", "origin", ref); e != nil {
		return "", e
	}
	prefix := ""
	if v.Subdir != "" {
		prefix = v.Subdir + "/"
	}
	body, e := a.git(ctx, temp, "show", "FETCH_HEAD:"+prefix+"SKILL.md")
	if e != nil {
		return "", e
	}
	name, e := declaredSkillName([]byte(body))
	if e == nil && install != nil {
		e = install(temp, name)
	}
	return name, e
}

func copySkillFiles(source, dest string) *Failure {
	err := filepath.WalkDir(source, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(source, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.Mkdir(target, 0755)
		}
		if !d.Type().IsRegular() {
			return fs.ErrInvalid
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644|info.Mode().Perm()&0111)
	})
	if err != nil {
		return ioFailure("Cannot copy Skill files; expected a tree of regular files and directories")
	}
	return nil
}

func (a *app) installTeamSkill(ctx context.Context, v SkillSource, dest string) *Failure {
	if v.Kind == "git" {
		return a.installGitSkill(ctx, v, dest)
	}
	temp, err := os.MkdirTemp("", "workbox-npx-")
	if err != nil {
		return ioFailure("Cannot stage npx installer")
	}
	defer os.RemoveAll(temp)
	project := filepath.Join(temp, "project")
	home := filepath.Join(temp, "home")
	for _, p := range []string{project, home} {
		if err := os.Mkdir(p, 0700); err != nil {
			return ioFailure("Cannot create installer directory")
		}
	}
	child, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(child, "npx", "--yes", "skills@1.7.6", "add", v.URL, "--skill", v.Name, "--agent", "codex", "--copy", "--yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	cmd.Dir = project
	cmd.Env = os.Environ()
	for key, path := range map[string]string{"HOME": home, "CODEX_HOME": filepath.Join(home, ".codex"), "XDG_CONFIG_HOME": filepath.Join(home, "config"), "XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_CACHE_HOME": filepath.Join(home, "cache"), "TMPDIR": temp} {
		cmd.Env = setEnv(cmd.Env, key, path)
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fail("INTERRUPTED", "Skill installation cancelled")
		}
		if child.Err() != nil {
			return fail("TIMEOUT", "Skill installation timed out")
		}
		if errors.Is(err, exec.ErrNotFound) {
			return fail("SKILL_INSTALL_FAILED", "npx is required to install "+v.Name)
		}
		return fail("SKILL_INSTALL_FAILED", "Could not install "+v.Name+" with npx skills; check npx and source access")
	}
	dir := filepath.Join(project, ".agents", "skills", v.Name)
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ioFailure("Installer did not produce the selected Skill")
	}
	root, err := filepath.EvalSymlinks(project)
	if err != nil || !strings.HasPrefix(actual, root+string(os.PathSeparator)) {
		return invalid("Skill leaves installer project")
	}
	return copySelectedSkill(actual, dest, v.Name)
}

// Git checkout filters and external installers can change a file during
// materialization. Validate the actual entry before copying its tree.
func copySelectedSkill(source, dest, expectedName string) *Failure {
	entry := filepath.Join(source, "SKILL.md")
	info, err := os.Lstat(entry)
	if err != nil || !info.Mode().IsRegular() {
		return invalid("Installed Skill requires a regular SKILL.md")
	}
	raw, err := os.ReadFile(entry)
	if err != nil {
		return invalid("Installed Skill lacks SKILL.md")
	}
	name, e := declaredSkillName(raw)
	if e != nil {
		return e
	}
	if name != expectedName {
		return invalid("Registered Skill " + expectedName + " materialized as " + name)
	}
	return copySkillFiles(source, dest)
}

func (a *app) skillCatalog(ctx context.Context, c command) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	if c.action == "list" {
		rows := []SkillSource{}
		for _, n := range names(s.Skills) {
			rows = append(rows, s.Skills[n])
		}
		return map[string]any{"skill_configs": rows}, nil
	}
	old, exists := s.Skills[c.name]
	if c.action != "add" && !exists {
		return nil, missing("skill_config", c.name)
	}
	if c.action == "show" {
		return old, nil
	}
	next := s.clone()
	if c.action == "remove" {
		used := []string{}
		for _, n := range names(s.Workspaces) {
			if slices.Contains(s.Workspaces[n].Skills, c.name) {
				used = append(used, n)
			}
		}
		if len(used) > 0 {
			e := fail("CONFIG_CONFLICT", "Skill is referenced by Workbox configurations")
			e.Details["workspace_configs"] = used
			return nil, e
		}
		delete(next.Skills, c.name)
		return map[string]string{"removed": c.name}, a.store.commit(ctx, s, next)
	}
	v := SkillSource{Name: "pending", Kind: "git", URL: c.flags["git-url"], Subdir: c.flags["subdir"], Branch: c.flags["branch"], Commit: c.flags["commit"]}
	if c.flags["installer"] != "" {
		v.Kind = c.flags["installer"]
		v.URL = c.flags["source"]
		v.Name = c.flags["skill"]
	}
	if e := validateSkillSource(v); e != nil {
		return nil, e
	}
	if v.Kind == "git" {
		v.Name, e = a.registerGitSkill(ctx, v)
		if e != nil {
			return nil, e
		}
	}
	if c.action == "update" && c.name != v.Name {
		return nil, invalid("Updated source must declare " + c.name)
	}
	if existing, ok := s.Skills[v.Name]; ok {
		if existing == v {
			return map[string]any{"state": "unchanged", "skill_config": existing}, nil
		}
		if c.action == "add" {
			return nil, fail("CONFIG_CONFLICT", "Registered Skill has different source: "+v.Name)
		}
	}
	next.Skills[v.Name] = v
	return map[string]any{"skill_config": v}, a.store.commit(ctx, s, next)
}

func (a *app) selectSkill(ctx context.Context, c command) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	row, ok := s.Workspaces[c.name]
	if !ok {
		return nil, missing("workspace_config", c.name)
	}
	name := c.flags["skill"]
	if _, ok := s.Skills[name]; !ok {
		return nil, missing("skill_config", name)
	}
	next := s.clone()
	row = next.Workspaces[c.name]
	idx := slices.Index(row.Skills, name)
	if c.action == "add-skill" && idx < 0 {
		row.Skills = append(row.Skills, name)
	}
	if c.action == "remove-skill" && idx >= 0 {
		row.Skills = slices.Delete(row.Skills, idx, idx+1)
		if len(row.Skills) == 0 {
			row.Skills = nil
		}
	}
	w, e := next.public(row)
	if e != nil {
		return nil, e
	}
	if e := validateWorkspace(w); e != nil {
		return nil, e
	}
	next.Workspaces[c.name] = row
	return map[string]any{"workspace_config": w}, a.store.commit(ctx, s, next)
}
