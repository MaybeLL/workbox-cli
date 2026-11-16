package workbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"reflect"
	"strings"

	"golang.org/x/text/cases"
)

func (a *app) instanceRepo(ctx context.Context, c command) (workspaceRepo, gitlabProject, *Failure) {
	var entry workspaceRepo
	if name := c.flags["repo"]; name != "" {
		store := a.store
		if store == nil {
			var e *Failure
			store, e = configuredStore()
			if e != nil {
				return entry, gitlabProject{}, e
			}
		}
		s, e := store.snapshot(ctx)
		if e != nil {
			return entry, gitlabProject{}, e
		}
		r, ok := s.Repos[name]
		if !ok {
			return entry, gitlabProject{}, missing("repo_config", name)
		}
		r, e = s.publicRepo(r)
		if e != nil {
			return entry, gitlabProject{}, e
		}
		entry = workspaceRepo{Name: r.Name, URL: r.URL, Summary: r.Summary, DefaultBranch: r.Branch}
		for _, key := range r.Teams {
			entry.Teams = append(entry.Teams, s.Teams[key])
		}
	} else {
		entry.URL = c.flags["url"]
		entry.Name = c.flags["name"]
	}
	project, e := a.gitlabProject(entry.URL)
	if e != nil {
		return entry, project, e
	}
	if entry.Name == "" {
		decoded, _ := url.PathUnescape(project.api)
		entry.Name = strings.ToLower(path.Base(decoded))
	}
	entry.Target = c.flags["target-dir"]
	if entry.Target == "" {
		entry.Target = path.Join("repos", entry.Name)
	}
	for _, kind := range []string{"branch", "commit"} {
		if v := c.flags[kind]; v != "" {
			entry.Ref = &BaselineRef{Type: kind, Value: v}
		}
	}
	return entry, project, nil
}

// Reject collisions with artifacts and documents from old instances too.
func vacantTarget(root *os.Root, target string) *Failure {
	parent := "."
	parts := strings.Split(target, "/")
	fold := cases.Fold()
	for i, part := range parts {
		dir, err := root.Open(parent)
		if err != nil {
			return ioFailure("Cannot inspect target parent")
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return ioFailure("Cannot inspect target directory")
		}
		found := false
		for _, d := range entries {
			if fold.String(d.Name()) != fold.String(part) {
				continue
			}
			if d.Name() != part || !d.IsDir() || i == len(parts)-1 {
				return fail("TARGET_EXISTS", "Checkout path conflicts with existing entry: "+path.Join(parent, d.Name()))
			}
			found = true
		}
		if !found {
			return nil
		}
		parent = path.Join(parent, part)
	}
	return nil
}

func (a *app) appendInstanceRepo(ctx context.Context, c command) (any, *Failure) {
	root, err := os.OpenRoot(c.flags["path"])
	if err != nil {
		return nil, ioFailure("Cannot open Workbox instance")
	}
	defer root.Close()
	info, err := root.Lstat(".workbox")
	if err != nil || !info.IsDir() {
		return nil, invalid("Expected an instance with a real .workbox directory")
	}
	meta, err := root.OpenRoot(".workbox")
	if err != nil {
		return nil, ioFailure("Cannot open instance metadata")
	}
	defer meta.Close()
	entry, project, e := a.instanceRepo(ctx, c)
	if e != nil {
		return nil, e
	}
	// Resolve metadata only for a new URL, without holding the instance lock
	// during network I/O. Recheck the complete current list before publishing.
	if entry.DefaultBranch == "" {
		repos, e := readInstanceManifest(meta)
		if e != nil {
			return nil, e
		}
		if existing, e := a.selectedInstanceRepo(repos, entry, project, c); existing != nil || e != nil {
			if e != nil {
				return nil, e
			}
			return map[string]any{"state": "unchanged", "repo": existing}, nil
		}
		entry.DefaultBranch, e = project.defaultBranch(ctx)
		if e != nil {
			return nil, e
		}
	}
	unlock, e := lockDirectory(ctx, meta)
	if e != nil {
		return nil, e
	}
	defer unlock()
	repos, e := readInstanceManifest(meta)
	if e != nil {
		return nil, e
	}
	if existing, e := a.selectedInstanceRepo(repos, entry, project, c); existing != nil || e != nil {
		if e != nil {
			return nil, e
		}
		return map[string]any{"state": "unchanged", "repo": existing}, nil
	}
	refs := []Ref{}
	for _, old := range repos {
		if old.Name == entry.Name || overlaps(cases.Fold().String(old.Target), cases.Fold().String(entry.Target)) {
			return nil, fail("CONFIG_CONFLICT", "Cannot add "+entry.Name+" at "+entry.Target+": conflicts with "+old.Name+" at "+old.Target)
		}
		refs = append(refs, Ref{Repo: old.Name, Target: old.Target, Ref: old.Ref})
	}
	refs = append(refs, Ref{Repo: entry.Name, Target: entry.Target, Ref: entry.Ref})
	if e := validateRefs(refs); e != nil {
		return nil, e
	}
	if overlaps(cases.Fold().String(entry.Target), ".agents/skills") {
		return nil, invalid("Checkout target overlaps instance Skills")
	}
	if e := vacantTarget(root, entry.Target); e != nil {
		return nil, e
	}
	data, err := json.MarshalIndent(append(repos, entry), "", "  ")
	if err != nil {
		return nil, invalid("Could not encode manifest")
	}
	id, e := uuid()
	if e != nil {
		return nil, e
	}
	tmp := ".repos-" + id
	f, err := meta.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, ioFailure("Cannot stage manifest")
	}
	defer meta.Remove(tmp)
	_, writeErr := f.Write(append(data, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if errors.Join(writeErr, syncErr, closeErr) != nil {
		return nil, ioFailure("Cannot save manifest")
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Instance update interrupted")
	}
	if err := meta.Rename(tmp, "repos.json"); err != nil {
		return nil, ioFailure("Cannot publish manifest")
	}
	return map[string]any{"state": "added", "repo": entry}, nil
}

func readInstanceManifest(meta *os.Root) ([]workspaceRepo, *Failure) {
	info, err := meta.Lstat("repos.json")
	if err != nil || !info.Mode().IsRegular() {
		return nil, invalid("Expected regular instance manifest")
	}
	raw, err := meta.ReadFile("repos.json")
	var repos []workspaceRepo
	if err != nil || json.Unmarshal(raw, &repos) != nil || repos == nil {
		return nil, invalid("Invalid instance manifest")
	}
	return repos, nil
}

func (a *app) selectedInstanceRepo(repos []workspaceRepo, entry workspaceRepo, project gitlabProject, c command) (*workspaceRepo, *Failure) {
	for _, old := range repos {
		prior, e := a.gitlabProject(old.URL)
		if entry.URL != old.URL && (e != nil || prior.host != project.host || prior.api != project.api) {
			continue
		}
		explicitName := c.flags["name"] != "" || c.flags["repo"] != ""
		if (explicitName && old.Name != entry.Name) || (c.flags["target-dir"] != "" && old.Target != entry.Target) || (entry.Ref != nil && !reflect.DeepEqual(old.Ref, entry.Ref)) {
			return nil, fail("CONFIG_CONFLICT", "Project is already selected as "+old.Name+" at "+old.Target)
		}
		return &old, nil
	}
	return nil, nil
}
