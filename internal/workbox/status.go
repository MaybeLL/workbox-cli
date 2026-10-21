package workbox

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type statusRepo struct {
	Kind   string   `json:"kind"`
	Path   string   `json:"path"`
	Branch *string  `json:"branch"`
	Commit *string  `json:"commit"`
	Clean  *bool    `json:"clean"`
	Error  *Failure `json:"error"`
}
type statusData struct {
	Path       string        `json:"workspace_path"`
	Repos      []*statusRepo `json:"repos"`
	ScanErrors []*Failure    `json:"scan_errors"`
}

func realDirectory(path string) bool { info, err := os.Lstat(path); return err == nil && info.IsDir() }
func canonical(path string) (string, *Failure) {
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ioFailure("Could not resolve Git path")
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", ioFailure("Could not resolve Git path")
	}
	return p, nil
}
func (a *app) gitPath(ctx context.Context, path, option string) (string, *Failure) {
	out, e := a.git(ctx, path, "rev-parse", "--path-format=absolute", option)
	if e != nil {
		return "", e
	}
	return canonical(out)
}
func (a *app) checkout(ctx context.Context, path, kind, relative string) *statusRepo {
	entry := &statusRepo{Kind: kind, Path: relative}
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		entry.Error = fail("PATH_INVALID", "Checkout .git must be a regular file or real directory")
		return entry
	}
	top, e := a.git(ctx, path, "rev-parse", "--show-toplevel")
	if e != nil {
		entry.Error = e
		return entry
	}
	resolved, e := canonical(top)
	if e != nil || resolved != path {
		entry.Error = fail("GIT_ERROR", "Candidate is not a checkout root")
		return entry
	}
	branch, rc, e := a.gitRunner.run(ctx, path, 60*time.Second, false, []int{0, 1}, "symbolic-ref", "--quiet", "--short", "HEAD")
	if e != nil {
		entry.Error = e
		return entry
	}
	if rc == 0 {
		entry.Branch = &branch
	}
	commit, rc, e := a.gitRunner.run(ctx, path, 60*time.Second, false, []int{0, 1, 128}, "rev-parse", "--verify", "HEAD")
	if e != nil {
		entry.Error = e
		return entry
	}
	if rc == 0 {
		entry.Commit = &commit
	} else {
		if entry.Branch == nil {
			entry.Error = fail("GIT_ERROR", "HEAD cannot be resolved")
			return entry
		}
		_, rc, e := a.gitRunner.run(ctx, path, 60*time.Second, false, []int{0, 1, 128}, "show-ref", "--verify", "--quiet", "refs/heads/"+*entry.Branch)
		if e != nil {
			entry.Error = e
			return entry
		}
		if rc != 1 {
			entry.Error = fail("GIT_ERROR", "HEAD refers to a damaged commit")
			return entry
		}
	}
	out, e := a.git(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		entry.Error = e
		return entry
	}
	entry.Clean = pointer(out == "")
	return entry
}
func (a *app) status(ctx context.Context, raw string) (any, *Failure) {
	path, e := absolute(raw)
	if e != nil {
		return nil, e
	}
	if !realDirectory(path) {
		return nil, fail("PATH_INVALID", "Status root must exist and be a real directory, not a symlink")
	}
	root, e := canonical(path)
	if e != nil {
		return nil, e
	}
	data := &statusData{Path: root, Repos: []*statusRepo{}, ScanErrors: []*Failure{}}
	bases := []string{}
	rel := func(path string) string { p, _ := filepath.Rel(root, path); return filepath.ToSlash(p) }
	scanError := func(path string, e *Failure) {
		e.Details["path"] = rel(path)
		data.ScanErrors = append(data.ScanErrors, e)
	}
	var scan func(string)
	scan = func(path string) {
		if ctx.Err() != nil {
			scanError(path, fail("INTERRUPTED", "Status interrupted"))
			return
		}
		if !realDirectory(path) {
			scanError(path, fail("PATH_INVALID", "Scanned directory was replaced or linked"))
			return
		}
		exists, err := pathExists(filepath.Join(path, ".git"))
		if err != nil {
			scanError(path, ioFailure("Could not inspect directory"))
			return
		}
		if exists {
			entry := a.checkout(ctx, path, "base", rel(path))
			data.Repos = append(data.Repos, entry)
			if entry.Error == nil {
				bases = append(bases, path)
			}
			return
		}
		children, err := os.ReadDir(path)
		if err != nil {
			scanError(path, ioFailure("Could not scan directory"))
			return
		}
		for _, child := range children {
			if child.Name() == ".git" || child.Name() == ".workbox" {
				continue
			}
			if child.IsDir() {
				scan(filepath.Join(path, child.Name()))
			}
		}
	}
	scan(root)
	owners := map[string]map[string]bool{}
	for _, base := range bases {
		common, e := a.gitPath(ctx, base, "--git-common-dir")
		if e != nil {
			scanError(base, e)
			continue
		}
		out, e := a.git(ctx, base, "worktree", "list", "--porcelain", "-z")
		if e != nil {
			scanError(base, e)
			continue
		}
		if owners[common] == nil {
			owners[common] = map[string]bool{}
		}
		for _, field := range strings.Split(out, "\x00") {
			if strings.HasPrefix(field, "worktree ") {
				path, e := canonical(strings.TrimPrefix(field, "worktree "))
				if e == nil {
					owners[common][path] = true
				}
			}
		}
	}
	container := filepath.Join(root, ".workbox")
	worktrees := filepath.Join(container, "worktree")
	candidates := []os.DirEntry{}
	if exists, err := pathExists(container); err != nil {
		scanError(container, ioFailure("Could not inspect .workbox"))
	} else if exists {
		if !realDirectory(container) {
			scanError(container, fail("PATH_INVALID", ".workbox must be a real directory"))
		} else if exists, err := pathExists(worktrees); err != nil {
			scanError(worktrees, ioFailure("Could not inspect worktree container"))
		} else if exists {
			if !realDirectory(worktrees) {
				scanError(worktrees, fail("PATH_INVALID", "Worktree container must be a real directory"))
			} else {
				var err error
				candidates, err = os.ReadDir(worktrees)
				if err != nil {
					scanError(worktrees, ioFailure("Could not read worktree container"))
				}
			}
		}
	}
	for _, candidate := range candidates {
		path := filepath.Join(worktrees, candidate.Name())
		var entryError *Failure
		if !realDirectory(path) {
			entryError = fail("PATH_INVALID", "Worktree candidate must be a real directory")
		} else {
			gitdir, e := a.gitPath(ctx, path, "--git-dir")
			if e != nil {
				entryError = e
			} else {
				common, e := a.gitPath(ctx, path, "--git-common-dir")
				if e != nil {
					entryError = e
				} else if gitdir == common || !owners[common][path] {
					entryError = fail("GIT_ERROR", "Linked worktree does not belong to a discovered baseline")
				} else {
					body, err := os.ReadFile(filepath.Join(gitdir, "gitdir"))
					if err != nil {
						entryError = fail("GIT_ERROR", "Cannot read worktree admin record")
					} else {
						linked := strings.TrimSuffix(string(body), "\n")
						if !filepath.IsAbs(linked) {
							linked = filepath.Join(gitdir, linked)
						}
						actual, e := canonical(linked)
						expected, e2 := canonical(filepath.Join(path, ".git"))
						if e != nil || e2 != nil || actual != expected {
							entryError = fail("GIT_ERROR", "Worktree admin record points to another checkout")
						}
					}
				}
			}
		}
		if entryError != nil {
			data.Repos = append(data.Repos, &statusRepo{Kind: "worktree", Path: rel(path), Error: entryError})
		} else {
			data.Repos = append(data.Repos, a.checkout(ctx, path, "worktree", rel(path)))
		}
	}
	sort.Slice(data.Repos, func(i, j int) bool { return data.Repos[i].Path < data.Repos[j].Path })
	sort.Slice(data.ScanErrors, func(i, j int) bool {
		return data.ScanErrors[i].Details["path"].(string) < data.ScanErrors[j].Details["path"].(string)
	})
	partial := len(data.ScanErrors) > 0
	for _, r := range data.Repos {
		partial = partial || r.Error != nil
	}
	if partial {
		e := fail("STATUS_PARTIAL", "Some directories could not be inspected")
		e.Data = data
		return nil, e
	}
	return data, nil
}
