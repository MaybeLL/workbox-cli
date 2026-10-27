package workbox

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"
)

func validateTeam(t Team) *Failure {
	if e := validateName(t.Key); e != nil {
		return e
	}
	if strings.TrimSpace(t.Name) == "" || !utf8.ValidString(t.Name) || strings.ContainsRune(t.Name, 0) {
		return invalid("Team name must be nonempty UTF-8 text without NUL")
	}
	if !utf8.ValidString(t.Summary) || strings.ContainsRune(t.Summary, 0) {
		return invalid("Team summary must be UTF-8 text without NUL")
	}
	return nil
}
func normalizeTeams(keys []string) ([]string, *Failure) {
	result := append([]string(nil), keys...)
	sort.Strings(result)
	for i, key := range result {
		if e := validateName(key); e != nil {
			return nil, e
		}
		if i > 0 && key == result[i-1] {
			return nil, invalid("Duplicate team key")
		}
	}
	return result, nil
}
func (s snapshot) publicRepo(r Repo) (Repo, *Failure) {
	r.Teams = nil
	byID := map[string]string{}
	for key, t := range s.Teams {
		byID[t.ID] = key
	}
	for _, id := range r.TeamIDs {
		key, ok := byID[id]
		if !ok {
			return r, invalid("Repository has an invalid team reference")
		}
		r.Teams = append(r.Teams, key)
	}
	var e *Failure
	r.Teams, e = normalizeTeams(r.Teams)
	r.TeamIDs = nil
	return r, e
}
func (s snapshot) internalRepo(r Repo) (Repo, *Failure) {
	keys, e := normalizeTeams(r.Teams)
	if e != nil {
		return r, e
	}
	r.TeamIDs = nil
	for _, key := range keys {
		team, ok := s.Teams[key]
		if !ok {
			return r, missing("team_config", key)
		}
		r.TeamIDs = append(r.TeamIDs, team.ID)
	}
	r.Teams = nil
	return r, nil
}
func (a *app) teamCRUD(ctx context.Context, c command) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	if c.action == "list" {
		rows := []Team{}
		for _, key := range names(s.Teams) {
			rows = append(rows, s.Teams[key])
		}
		return map[string]any{"team_configs": rows}, nil
	}
	if e := validateName(c.name); e != nil {
		return nil, e
	}
	t, exists := s.Teams[c.name]
	if c.action == "add" && exists {
		return nil, fail("CONFIG_CONFLICT", "Team key already exists")
	}
	if c.action != "add" && !exists {
		return nil, missing("team_config", c.name)
	}
	if c.action == "show" {
		return map[string]any{"team_config": t}, nil
	}
	next := s.clone()
	if c.action == "remove" {
		for _, r := range s.Repos {
			for _, id := range r.TeamIDs {
				if id == t.ID {
					return nil, fail("CONFIG_CONFLICT", "Team is referenced by repository configurations")
				}
			}
		}
		delete(next.Teams, c.name)
	} else {
		t.Key = c.name
		if v, ok := c.flags["name"]; ok {
			t.Name = v
		}
		if v, ok := c.flags["summary"]; ok {
			t.Summary = v
		}
		if e := validateTeam(t); e != nil {
			return nil, e
		}
		if !exists {
			t.ID, e = uuid()
			if e != nil {
				return nil, e
			}
		}
		next.Teams[c.name] = t
	}
	if e := a.store.commit(ctx, s, next); e != nil {
		return nil, e
	}
	if c.action == "remove" {
		return map[string]any{"removed": map[string]string{"entity": "team_config", "key": c.name}}, nil
	}
	return map[string]any{"team_config": t}, nil
}
func (a *app) repoTeam(ctx context.Context, c command) (any, *Failure) {
	s, e := a.store.snapshot(ctx)
	if e != nil {
		return nil, e
	}
	r, ok := s.Repos[c.name]
	if !ok {
		return nil, missing("repo_config", c.name)
	}
	key := c.flags["team"]
	if _, ok := s.Teams[key]; !ok {
		return nil, missing("team_config", key)
	}
	r, e = s.publicRepo(r)
	if e != nil {
		return nil, e
	}
	keys := []string{}
	for _, old := range r.Teams {
		if old != key {
			keys = append(keys, old)
		}
	}
	if c.action == "add-team" {
		keys = append(keys, key)
	}
	r.Teams = keys
	stored, e := s.internalRepo(r)
	if e != nil {
		return nil, e
	}
	next := s.clone()
	next.Repos[r.Name] = stored
	if e := a.store.commit(ctx, s, next); e != nil {
		return nil, e
	}
	r, e = next.publicRepo(stored)
	return map[string]any{"repo_config": r}, e
}
