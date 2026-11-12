package workbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// workspaceRepo describes configured context; target_dir may not exist.
type workspaceRepo struct {
	Name          string       `json:"name"`
	URL           string       `json:"source_url"`
	Summary       string       `json:"summary"`
	Teams         []Team       `json:"teams,omitempty"`
	Target        string       `json:"target_dir"`
	DefaultBranch string       `json:"default_branch"`
	Ref           *BaselineRef `json:"ref,omitempty"`
}

func pointer[T any](value T) *T { return &value }
func destination(raw string) (string, *Failure) {
	path, e := absolute(raw)
	if e != nil {
		return "", e
	}
	exists, err := pathExists(path)
	if err != nil {
		return "", ioFailure("Could not inspect Workbox target")
	}
	if exists {
		return "", fail("TARGET_EXISTS", "Workbox target must not exist")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fail("PATH_INVALID", "Workbox parent must exist and be accessible")
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", fail("PATH_INVALID", "Workbox parent must be a directory")
	}
	if syscall.Access(parent, 3) != nil {
		return "", ioFailure("Workbox parent is not writable")
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// Root confines file and directory creation if a component is concurrently linked.
type ownedTree struct {
	path  string
	root  *os.Root
	owned map[string]os.FileInfo
}

func openInstanceParent(parentPath string) (*os.Root, *Failure) {
	before, err := os.Lstat(parentPath)
	resolved, resolveErr := filepath.EvalSymlinks(parentPath)
	if err != nil || resolveErr != nil || !before.IsDir() || resolved != parentPath {
		return nil, fail("PATH_INVALID", "Workbox parent was replaced or linked")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, ioFailure("Could not open Workbox parent")
	}

	actualParent, err := parent.Stat(".")
	if err != nil || !os.SameFile(before, actualParent) {
		parent.Close()
		return nil, fail("PATH_INVALID", "Workbox parent was replaced")
	}
	return parent, nil
}
func newTreeAt(parent *os.Root, path string) (*ownedTree, *Failure) {
	name := filepath.Base(path)
	if err := parent.Mkdir(name, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fail("TARGET_EXISTS", "Workbox target appeared during creation")
		}
		return nil, ioFailure("Could not create Workbox root")
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() {
		return nil, fail("PATH_INVALID", "Workbox root was replaced or linked")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, ioFailure("Could not open prepared Workbox root")
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fail("PATH_INVALID", "Workbox root was replaced")
	}
	return &ownedTree{path: path, root: root, owned: map[string]os.FileInfo{".": info}}, nil
}
func (t *ownedTree) check() *Failure {
	actual, err := os.Lstat(t.path)
	if err != nil || !actual.IsDir() || !os.SameFile(actual, t.owned["."]) {
		return fail("PATH_INVALID", "Workbox root was replaced or linked")
	}
	resolved, err := filepath.EvalSymlinks(t.path)
	if err != nil || resolved != t.path {
		return fail("PATH_INVALID", "Workbox parent was linked")
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
			// The filesystem may resolve a different spelling to an owned directory.
			info, statErr := t.root.Lstat(name)
			if statErr == nil && info.IsDir() {
				for _, owned := range t.owned {
					if os.SameFile(info, owned) {
						t.owned[name] = info
						return t.check()
					}
				}
			}
			return fail("TARGET_EXISTS", "Unexpected directory appeared during creation")
		}
		return ioFailure("Could not prepare Workbox directory")
	}
	info, err := t.root.Lstat(name)
	if err != nil || !info.IsDir() {
		return fail("PATH_INVALID", "Unexpected link in Workbox")
	}
	t.owned[name] = info
	return t.check()
}

// publishedInstance is returned only after exclusive directory publication succeeds.
type publishedInstance map[string]any

func (a *app) prepare(ctx context.Context, name, raw string, create bool) (data any, failure *Failure) {
	result := map[string]any{"workspace_path": nil, "config_name": name}
	repos := []workspaceRepo{}
	result["repos"] = repos
	if create {
		result["state"] = "failed"
		result["init_script"] = nil
	}
	stage := "snapshot"
	defer func() {
		if failure != nil {
			if _, ok := failure.Details["stage"]; !ok {
				failure.Details["stage"] = stage
			}
			failure.Data = result
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
		public, e := s.publicRepo(repo)
		if e != nil {
			return nil, e
		}
		entry := workspaceRepo{
			Name:          repo.Name,
			URL:           repo.URL,
			Summary:       repo.Summary,
			Target:        ref.Target,
			DefaultBranch: repo.Branch,
			Ref:           ref.Ref,
		}
		for _, key := range public.Teams {
			entry.Teams = append(entry.Teams, s.Teams[key])
		}
		repos = append(repos, entry)
	}
	result["repos"] = repos
	selectedSkills := []SkillSource{}
	for _, n := range w.Skills {
		v, ok := s.Skills[n]
		if !ok {
			return nil, missing("skill_config", n)
		}
		selectedSkills = append(selectedSkills, v)
	}
	result["skills"] = selectedSkills
	result["docs"] = append([]Document{}, w.Docs...)
	stage = "path_preflight"
	root, e := destination(raw)
	if e != nil {
		return nil, e
	}
	result["workspace_path"] = root
	if !create {
		return result, nil
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Workbox preparation interrupted")
	}
	if e := a.createInstance(ctx, root, repos, w.Docs, selectedSkills); e != nil {
		return nil, e
	}
	result["state"] = "prepared"
	return publishedInstance(result), nil
}

// createInstance owns staging, rollback and the single visible commit point.
func (a *app) createInstance(ctx context.Context, root string, repos []workspaceRepo, docs []Document, selectedSkills []SkillSource) (failure *Failure) {
	stage := "mkdir"
	defer func() {
		if failure != nil {
			failure.Details["stage"] = stage
		}
	}()
	parentPath := filepath.Dir(root)
	parent, e := openInstanceParent(parentPath)
	if e != nil {
		return e
	}
	defer parent.Close()
	id, e := uuid()
	if e != nil {
		return e
	}
	stageName := ".workbox-create-" + id
	tree, e := newTreeAt(parent, filepath.Join(parentPath, stageName))
	if e != nil {
		return e
	}
	defer tree.root.Close()
	// Remove only the stage we created, even if another creator wins publication.
	defer func() {
		info, err := parent.Lstat(stageName)
		if err == nil && os.SameFile(info, tree.owned["."]) {
			_ = parent.RemoveAll(stageName)
		}
	}()
	if e := tree.mkdir(".workbox"); e != nil {
		return e
	}
	stage = "repo_manifest"
	body, err := json.MarshalIndent(repos, "", "  ")
	if err != nil {
		return ioFailure("Could not encode repository list")
	}
	if e := tree.writeFile(".workbox/repos.json", append(body, '\n')); e != nil {
		return e
	}
	stage = "documents"
	if e := a.prepareDocuments(ctx, tree, docs); e != nil {
		return e
	}
	stage = "skills"
	if len(selectedSkills) > 0 {
		for _, dir := range []string{".agents", ".agents/skills"} {
			if e := tree.mkdir(dir); e != nil {
				return e
			}
		}
	}
	for _, v := range selectedSkills {
		if e := a.installTeamSkill(ctx, v, filepath.Join(tree.path, ".agents", "skills", v.Name)); e != nil {
			e.Details["skill"] = v.Name
			return e
		}
	}
	stage = "publish"
	if e := tree.check(); e != nil {
		return e
	}
	if ctx.Err() != nil {
		return fail("INTERRUPTED", "Workbox preparation interrupted")
	}
	publish := a.publishInstance
	if publish == nil {
		publish = publishDir
	}
	if err := publish(parent, stageName, filepath.Base(root)); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("TARGET_EXISTS", "Workbox target appeared during creation")
		}
		return ioFailure("Could not publish Workbox directory")
	}
	return nil
}

func (t *ownedTree) writeFile(name string, body []byte) *Failure {
	if e := t.check(); e != nil {
		return e
	}
	f, err := t.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("TARGET_EXISTS", "Workbox file already exists; nothing was overwritten")
		}
		return ioFailure("Could not create Workbox file")
	}
	_, writeErr := f.Write(body)
	info, statErr := f.Stat()
	closeErr := f.Close()
	if writeErr != nil || statErr != nil || closeErr != nil {
		return ioFailure("Could not write Workbox file")
	}
	t.owned[name] = info
	return t.check()
}
