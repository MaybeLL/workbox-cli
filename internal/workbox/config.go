package workbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (a *app) crud(ctx context.Context, c command) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	entity := "workspace_config"
	if c.entity == "repo-config" {
		entity = "repo_config"
	}
	if c.action == "list" {
		if entity == "repo_config" {
			rows := []Repo{}
			for _, name := range names(s.Repos) {
				rows = append(rows, s.Repos[name])
			}
			return map[string]any{"repo_configs": rows}, nil
		}
		rows := []Workspace{}
		for _, name := range names(s.Workspaces) {
			w, e := s.public(s.Workspaces[name])
			if e != nil {
				return nil, e
			}
			rows = append(rows, w)
		}
		return map[string]any{"workspace_configs": rows}, nil
	}
	if e := validateName(c.name); e != nil {
		return nil, e
	}
	r, hasRepo := s.Repos[c.name]
	w, hasWorkspace := s.Workspaces[c.name]
	exists := hasWorkspace
	if entity == "repo_config" {
		exists = hasRepo
	}
	if c.action != "add" && !exists {
		return nil, missing(entity, c.name)
	}
	if c.action == "add" && exists {
		return nil, fail("CONFIG_CONFLICT", "Configuration name already exists")
	}
	if c.action == "show" {
		if entity == "repo_config" {
			return map[string]any{entity: r}, nil
		}
		result, e := s.public(w)
		if e != nil {
			return nil, e
		}
		return map[string]any{entity: result}, nil
	}
	if c.action == "remove" {
		if entity == "repo_config" {
			used := []string{}
			for _, name := range names(s.Workspaces) {
				for _, ref := range s.Workspaces[name].Repos {
					if ref.ID == r.ID {
						used = append(used, name)
						break
					}
				}
			}
			if len(used) > 0 {
				e := fail("CONFIG_CONFLICT", "Repository is referenced by workspace configurations")
				e.Details["workspace_configs"] = used
				return nil, e
			}
		}
		next := s.clone()
		if entity == "repo_config" {
			delete(next.Repos, c.name)
		} else {
			delete(next.Workspaces, c.name)
		}
		e := a.store.commit(ctx, s, next)
		if e != nil {
			return nil, e
		}
		return map[string]any{"removed": map[string]string{"entity": entity, "name": c.name}}, nil
	}
	if c.action == "update" && len(c.flags) == 0 {
		return nil, fail("INVALID_ARGUMENTS", "update requires at least one changed field")
	}
	if entity == "repo_config" {
		r.Name = c.name
		if value, ok := c.flags["url"]; ok {
			r.URL = value
		}
		if value, ok := c.flags["default-branch"]; ok {
			r.Branch = value
		}
		if e := a.validateRepo(ctx, r); e != nil {
			return nil, e
		}
		if e := a.verifyRemote(ctx, r); e != nil {
			return nil, e
		}
		if !exists {
			r.ID, e = uuid()
			if e != nil {
				return nil, e
			}
		}
		next := s.clone()
		next.Repos[r.Name] = r
		e = a.store.commit(ctx, s, next)
		if e != nil {
			return nil, e
		}
		return map[string]any{entity: r}, nil
	}
	value := Workspace{Name: c.name, Repos: []Ref{}}
	if exists {
		value, e = s.public(w)
		if e != nil {
			return nil, e
		}
	}
	if file, ok := c.flags["repos-file"]; ok {
		raw, e := readText(file)
		if e != nil {
			return nil, e
		}
		if e := checkJSON(raw); e != nil {
			return nil, e
		}
		value.Repos, e = parseRefs(raw)
		if e != nil {
			return nil, e
		}
	}
	if file, ok := c.flags["init-script-file"]; ok {
		raw, e := readText(file)
		if e != nil {
			return nil, e
		}
		value.Script = string(raw)
	}
	if e := validateWorkspace(value); e != nil {
		return nil, e
	}
	refs, e := s.internal(value.Repos)
	if e != nil {
		return nil, e
	}
	if !exists {
		value.ID, e = uuid()
		if e != nil {
			return nil, e
		}
	}
	w = workspaceRow{ID: value.ID, Name: value.Name, Repos: refs, Script: value.Script}
	next := s.clone()
	next.Workspaces[w.Name] = w
	e = a.store.commit(ctx, s, next)
	if e != nil {
		return nil, e
	}
	return map[string]any{entity: value}, nil
}

func (a *app) export(ctx context.Context, output string) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	p, e := s.portable()
	if e != nil {
		return nil, e
	}
	target, e := absolute(output)
	if e != nil {
		return nil, e
	}
	exists, err := pathExists(target)
	if err != nil {
		return nil, ioFailure("Could not inspect export output")
	}
	if exists {
		return nil, fail("TARGET_EXISTS", "Export output already exists")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return nil, ioFailure("Export parent must exist and be accessible")
	}
	target = filepath.Join(parent, filepath.Base(target))
	temp, err := os.CreateTemp(parent, ".workbox-export-")
	if err != nil {
		return nil, ioFailure("Could not create export temporary file")
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(p); err != nil {
		return nil, ioFailure("Could not write export file")
	}
	if err := temp.Sync(); err != nil {
		return nil, ioFailure("Could not sync export file")
	}
	if err := temp.Close(); err != nil {
		return nil, ioFailure("Could not close export file")
	}
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Export interrupted")
	}
	if err := os.Link(temp.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fail("TARGET_EXISTS", "Export output appeared during publication")
		}
		return nil, ioFailure("Could not atomically publish export file")
	}
	return map[string]any{"output_path": target, "schema_version": 1}, nil
}
func (a *app) importFile(ctx context.Context, input string) (any, *Failure) {
	raw, e := readText(input)
	if e != nil {
		return nil, e
	}
	incoming, e := a.parsePackage(ctx, raw)
	if e != nil {
		return nil, e
	}
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	local, e := s.portable()
	if e != nil {
		return nil, e
	}
	added := map[string][]string{"repo_configs": {}, "workspace_configs": {}}
	unchanged := map[string][]string{"repo_configs": {}, "workspace_configs": {}}
	localRepos := map[string]Repo{}
	for _, r := range local.Repos {
		localRepos[r.Name] = r
	}
	localWorkspaces := map[string]Workspace{}
	for _, w := range local.Workspaces {
		localWorkspaces[w.Name] = w
	}
	for _, r := range incoming.Repos {
		old, ok := localRepos[r.Name]
		if ok && !jsonEqual(old, r) {
			return nil, fail("CONFIG_CONFLICT", "Imported repository conflicts with local content")
		}
		if ok {
			unchanged["repo_configs"] = append(unchanged["repo_configs"], r.Name)
		} else {
			added["repo_configs"] = append(added["repo_configs"], r.Name)
		}
	}
	for _, w := range incoming.Workspaces {
		old, ok := localWorkspaces[w.Name]
		if ok && !jsonEqual(old, w) {
			return nil, fail("CONFIG_CONFLICT", "Imported workspace conflicts with local content")
		}
		if ok {
			unchanged["workspace_configs"] = append(unchanged["workspace_configs"], w.Name)
		} else {
			added["workspace_configs"] = append(added["workspace_configs"], w.Name)
		}
	}
	for _, r := range incoming.Repos {
		if e := a.verifyRemote(ctx, r); e != nil {
			return nil, e
		}
	}
	result := map[string]any{"schema_version": 1, "added": added, "unchanged": unchanged}
	if len(incoming.Repos) == 0 && len(incoming.Workspaces) == 0 {
		return result, nil
	}
	next := s.clone()
	for _, r := range incoming.Repos {
		if _, ok := next.Repos[r.Name]; ok {
			continue
		}
		id, e := uuid()
		if e != nil {
			return nil, e
		}
		r.ID = id
		next.Repos[r.Name] = r
	}
	for _, w := range incoming.Workspaces {
		if _, ok := next.Workspaces[w.Name]; ok {
			continue
		}
		id, e := uuid()
		if e != nil {
			return nil, e
		}
		refs, e := next.internal(w.Repos)
		if e != nil {
			return nil, e
		}
		next.Workspaces[w.Name] = workspaceRow{ID: id, Name: w.Name, Repos: refs, Script: w.Script}
	}
	e = a.store.commit(ctx, s, next)
	if e != nil {
		return nil, e
	}
	return result, nil
}
