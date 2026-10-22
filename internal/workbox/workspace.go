package workbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type preparedRepo struct {
	Name          string   `json:"name"`
	Path          *string  `json:"path"`
	DefaultBranch string   `json:"default_branch"`
	State         string   `json:"state"`
	Branch        *string  `json:"branch"`
	Commit        *string  `json:"commit"`
	Error         *Failure `json:"error"`
}
type plannedRepo struct {
	Name          string         `json:"name"`
	URL           string         `json:"source_url"`
	DefaultBranch string         `json:"default_branch"`
	Target        string         `json:"target_dir"`
	Path          *string        `json:"path"`
	Checks        map[string]any `json:"checks"`
}

func pointer[T any](value T) *T { return &value }
func destination(raw string) (string, *Failure) {
	path, e := absolute(raw)
	if e != nil {
		return "", e
	}
	exists, err := pathExists(path)
	if err != nil {
		return "", ioFailure("Could not inspect workspace target")
	}
	if exists {
		return "", fail("TARGET_EXISTS", "Workspace target must not exist")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fail("PATH_INVALID", "Workspace parent must exist and be accessible")
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", fail("PATH_INVALID", "Workspace parent must be a directory")
	}
	if syscall.Access(parent, 3) != nil {
		return "", ioFailure("Workspace parent is not writable")
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// Root confines file and directory creation if a component is concurrently linked.
// Git still consumes paths, so this is not complete isolation from concurrent writers.
type ownedTree struct {
	path  string
	root  *os.Root
	owned map[string]os.FileInfo
}

func newTree(path string) (*ownedTree, *Failure) {
	parentPath := filepath.Dir(path)
	before, err := os.Lstat(parentPath)
	resolved, resolveErr := filepath.EvalSymlinks(parentPath)
	if err != nil || resolveErr != nil || !before.IsDir() || resolved != parentPath {
		return nil, fail("PATH_INVALID", "Workspace parent was replaced or linked")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, ioFailure("Could not open workspace parent")
	}
	defer parent.Close()
	actualParent, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, actualParent) {
		return nil, fail("PATH_INVALID", "Workspace parent was replaced")
	}
	name := filepath.Base(path)
	if err := parent.Mkdir(name, 0755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fail("TARGET_EXISTS", "Workspace target appeared during creation")
		}
		return nil, ioFailure("Could not create workspace root")
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() {
		return nil, fail("PATH_INVALID", "Workspace root was replaced or linked")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, ioFailure("Could not open prepared workspace root")
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fail("PATH_INVALID", "Workspace root was replaced")
	}
	return &ownedTree{path: path, root: root, owned: map[string]os.FileInfo{".": info}}, nil
}
func (t *ownedTree) check() *Failure {
	actual, err := os.Lstat(t.path)
	if err != nil || !actual.IsDir() || !os.SameFile(actual, t.owned["."]) {
		return fail("PATH_INVALID", "Workspace root was replaced or linked")
	}
	resolved, err := filepath.EvalSymlinks(t.path)
	if err != nil || resolved != t.path {
		return fail("PATH_INVALID", "Workspace parent was linked")
	}
	for name, info := range t.owned {
		actual, err := t.root.Lstat(name)
		if err != nil || actual.Mode().Type() != info.Mode().Type() || !os.SameFile(info, actual) {
			return fail("PATH_INVALID", "Prepared entry was replaced or linked")
		}
	}
	return nil
}
func (t *ownedTree) mkdir(name string) *Failure {
	if e := t.check(); e != nil {
		return e
	}
	if _, ok := t.owned[name]; ok {
		return nil
	}
	if err := t.root.Mkdir(name, 0755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("TARGET_EXISTS", "Unexpected directory appeared during creation")
		}
		return ioFailure("Could not prepare workspace directory")
	}
	info, err := t.root.Lstat(name)
	if err != nil || !info.IsDir() {
		return fail("PATH_INVALID", "Unexpected link in workspace")
	}
	t.owned[name] = info
	return t.check()
}
func (t *ownedTree) parents(target string) *Failure {
	parts := strings.Split(target, "/")
	for i := 1; i < len(parts); i++ {
		if e := t.mkdir(filepath.Join(parts[:i]...)); e != nil {
			return e
		}
	}
	return t.check()
}
func (a *app) prepare(ctx context.Context, name, raw string, create bool) (data any, failure *Failure) {
	result := map[string]any{"workspace_path": nil, "config_name": name}
	plans := []*plannedRepo{}
	prepared := []*preparedRepo{}
	if create {
		result["state"] = "failed"
		result["init_script"] = nil
		result["repos"] = prepared
	} else {
		result["repos"] = plans
	}
	stage := "snapshot"
	var active *preparedRepo
	defer func() {
		if failure != nil {
			failure.Details["stage"] = stage
			failure.Data = result
			if active != nil {
				active.State = "failed"
				active.Error = failure
			}
		}
	}()
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	row, ok := s.Workspaces[name]
	if !ok {
		return nil, missing("workspace_config", name)
	}
	w, e := s.public(row)
	if e != nil {
		return nil, e
	}
	if e := validateWorkspace(w); e != nil {
		return nil, e
	}
	if create {
		result["init_script"] = w.Script
	}
	for _, ref := range w.Repos {
		repo, ok := s.Repos[ref.Repo]
		if !ok {
			return nil, missing("repo_config", ref.Repo)
		}
		if e := a.validateRepo(ctx, repo); e != nil {
			return nil, e
		}
		if create {
			prepared = append(prepared, &preparedRepo{Name: repo.Name, DefaultBranch: repo.Branch, State: "not_attempted"})
		} else {
			plans = append(plans, &plannedRepo{Name: repo.Name, URL: repo.URL, DefaultBranch: repo.Branch, Target: ref.Target, Checks: map[string]any{"ok": nil}})
		}
	}
	if create {
		result["repos"] = prepared
	} else {
		result["repos"] = plans
	}
	stage = "path_preflight"
	root, e := destination(raw)
	if e != nil {
		return nil, e
	}
	result["workspace_path"] = root
	for i, ref := range w.Repos {
		path := filepath.Join(root, filepath.FromSlash(ref.Target))
		if create {
			prepared[i].Path = &path
		} else {
			plans[i].Path = &path
		}
	}
	stage = "remote_preflight"
	for i, ref := range w.Repos {
		if e := a.verifyRemote(ctx, s.Repos[ref.Repo]); e != nil {
			if !create {
				plans[i].Checks = map[string]any{"ok": false, "error": e}
			}
			return nil, e
		}
		if !create {
			plans[i].Checks = map[string]any{"ok": true}
		}
	}
	if !create {
		return result, nil
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Workspace preparation interrupted")
	}
	stage = "mkdir"
	tree, e := newTree(root)
	if e != nil {
		return nil, e
	}
	defer tree.root.Close()
	for _, dir := range []string{".workbox", filepath.Join(".workbox", "worktree")} {
		if e := tree.mkdir(dir); e != nil {
			return nil, e
		}
	}
	stage = "agent_instructions"
	if e := tree.writeAgentInstructions(); e != nil {
		return nil, e
	}
	stage = "clone"
	for i, ref := range w.Repos {
		active = prepared[i]
		repo := s.Repos[ref.Repo]
		if e := tree.parents(ref.Target); e != nil {
			return nil, e
		}
		exists, err := pathExists(*active.Path)
		if err != nil {
			return nil, ioFailure("Could not inspect repository target")
		}
		if exists {
			return nil, fail("TARGET_EXISTS", "Repository target appeared before clone")
		}
		if _, _, e := a.gitRunner.run(ctx, "", 1800*time.Second, false, []int{0}, "clone", "--single-branch", "--branch", repo.Branch, "--", repo.URL, *active.Path); e != nil {
			return nil, e
		}
		if e := tree.check(); e != nil {
			return nil, e
		}
		info, err := tree.root.Lstat(filepath.FromSlash(ref.Target))
		if err != nil || !info.IsDir() {
			return nil, fail("PATH_INVALID", "Repository target is not a real directory")
		}
		tree.owned[filepath.FromSlash(ref.Target)] = info
		branch, e := a.git(ctx, *active.Path, "symbolic-ref", "--short", "HEAD")
		if e != nil {
			return nil, e
		}
		active.Branch = &branch
		commit, e := a.git(ctx, *active.Path, "rev-parse", "--verify", "HEAD")
		if e != nil {
			return nil, e
		}
		active.Commit = &commit
		if branch != repo.Branch {
			return nil, fail("GIT_ERROR", "Clone checked out an unexpected branch")
		}
		active.State = "completed"
		active = nil
	}
	if e := tree.check(); e != nil {
		return nil, e
	}
	result["state"] = "prepared"
	return result, nil
}
