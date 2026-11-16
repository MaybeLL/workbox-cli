package workbox

import (
	"context"
	"reflect"
	"slices"
)

// Parse replacement fields once. A retry must apply the same input, even if
// the caller later edits its JSON files. A nil pointer leaves a field alone.
type workspaceChange struct {
	repos       *[]Ref
	docs        *[]Document
	script      *string
	skill       string
	selectSkill bool
}

func parseWorkspaceChange(c command) (workspaceChange, *Failure) {
	var change workspaceChange
	if file, ok := c.flags["docs-file"]; ok {
		raw, e := readText(file)
		if e != nil {
			return change, e
		}
		docs, e := parseDocuments(raw)
		if e != nil {
			return change, e
		}
		change.docs = &docs
	}
	if file, ok := c.flags["repos-file"]; ok {
		raw, e := readText(file)
		if e != nil {
			return change, e
		}
		if e := checkJSON(raw); e != nil {
			return change, e
		}
		refs, e := parseRefs(raw)
		if e != nil {
			return change, e
		}
		change.repos = &refs
	}
	if file, ok := c.flags["init-script-file"]; ok {
		raw, e := readText(file)
		if e != nil {
			return change, e
		}
		script := string(raw)
		change.script = &script
	}
	switch c.action {
	case "add-skill", "remove-skill":
		change.skill = c.flags["skill"]
		change.selectSkill = c.action == "add-skill"
		if e := validateName(change.skill); e != nil {
			return change, e
		}
	}
	return change, nil
}

func (c workspaceChange) apply(s snapshot, w *Workspace) (workspaceRow, *Failure) {
	if c.docs != nil {
		w.Docs = *c.docs
	}
	if c.repos != nil {
		w.Repos = *c.repos
	}
	if c.script != nil {
		w.Script = *c.script
	}
	if c.skill != "" {
		if _, ok := s.Skills[c.skill]; !ok {
			return workspaceRow{}, missing("skill_config", c.skill)
		}
		index := slices.Index(w.Skills, c.skill)
		switch {
		case c.selectSkill && index < 0:
			w.Skills = append(w.Skills, c.skill)
		case !c.selectSkill && index >= 0:
			w.Skills = slices.Delete(w.Skills, index, index+1)
			if len(w.Skills) == 0 {
				w.Skills = nil
			}
		}
	}
	if e := validateWorkspace(*w); e != nil {
		return workspaceRow{}, e
	}
	refs, e := s.internal(w.Repos)
	if e != nil {
		return workspaceRow{}, e
	}
	return workspaceRow{ID: w.ID, Name: w.Name, Repos: refs, Docs: w.Docs, Script: w.Script, Skills: w.Skills}, nil
}

// updateWorkspace commits a change against one configuration's revision.
// Contention elsewhere in the catalog is transparent to the caller.
func updateWorkspace(ctx context.Context, store configStore, name, revision string, change workspaceChange) (Workspace, *Failure) {
	if e := validateName(name); e != nil {
		return Workspace{}, e
	}
	for ctx.Err() == nil {
		s, e := store.snapshot(ctx)
		if e != nil {
			return Workspace{}, e
		}
		old, ok := s.Workspaces[name]
		if !ok {
			return Workspace{}, missing("workspace_config", name)
		}
		if old.revision() != revision {
			return Workspace{}, staleWorkspace()
		}
		value, e := s.public(old)
		if e != nil {
			return Workspace{}, e
		}
		row, e := change.apply(s, &value)
		if e != nil {
			return Workspace{}, e
		}
		row.Generation = old.Generation
		if !reflect.DeepEqual(row, old) {
			row.Generation++
		}
		next := s.clone()
		next.Workspaces[name] = row
		if e := store.commit(ctx, s, next); e != nil {
			if e.Details["reason"] == "snapshot_changed" {
				continue
			}
			return Workspace{}, e
		}
		value.Revision = row.revision()
		return value, nil
	}
	return Workspace{}, fail("INTERRUPTED", "Configuration update interrupted")
}
