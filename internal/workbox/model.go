package workbox

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
)

type Failure struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
	Data    any            `json:"-"`
}

func fail(code, message string) *Failure {
	return &Failure{Code: code, Message: message, Details: map[string]any{}}
}
func (f *Failure) Error() string { return f.Message }
func (f *Failure) ExitCode() int {
	switch f.Code {
	case "INTERRUPTED":
		return 130
	case "INVALID_ARGUMENTS", "CONFIG_INVALID", "CONFIG_NOT_FOUND", "CONFIG_CONFLICT", "SCHEMA_UNSUPPORTED", "PATH_INVALID", "TARGET_EXISTS", "REMOTE_REPO_NOT_FOUND", "REMOTE_BRANCH_NOT_FOUND", "SKILL_CONFLICT":
		return 2
	default:
		return 1
	}
}
func ioFailure(message string) *Failure { return fail("IO_ERROR", message) }

type Repo struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	URL    string `json:"source_url"`
	Branch string `json:"default_branch"`
}
type Ref struct {
	Repo   string `json:"repo"`
	Target string `json:"target_dir"`
}
type StoredRef struct {
	ID     string `json:"repo_config_id"`
	Target string `json:"target_dir"`
}
type Workspace struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Repos  []Ref  `json:"repos"`
	Script string `json:"init_script"`
}
type workspaceRow struct {
	ID, Name, Script string
	Repos            []StoredRef
}
type snapshot struct {
	Repos      map[string]Repo
	Workspaces map[string]workspaceRow
}

func emptySnapshot() snapshot { return snapshot{map[string]Repo{}, map[string]workspaceRow{}} }

type Package struct {
	Version    int         `json:"schema_version"`
	Repos      []Repo      `json:"repo_configs"`
	Workspaces []Workspace `json:"workspace_configs"`
}

func names[T any](m map[string]T) []string {
	result := make([]string, 0, len(m))
	for name := range m {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
func (s snapshot) public(row workspaceRow) (Workspace, *Failure) {
	w := Workspace{ID: row.ID, Name: row.Name, Script: row.Script, Repos: []Ref{}}
	byID := map[string]string{}
	for _, repo := range s.Repos {
		byID[repo.ID] = repo.Name
	}
	for _, ref := range row.Repos {
		name, ok := byID[ref.ID]
		if !ok {
			return w, fail("CONFIG_INVALID", "Workspace has invalid repository references")
		}
		w.Repos = append(w.Repos, Ref{name, ref.Target})
	}
	return w, nil
}
func (s snapshot) portable() (Package, *Failure) {
	p := Package{Version: 1, Repos: []Repo{}, Workspaces: []Workspace{}}
	for _, name := range names(s.Repos) {
		r := s.Repos[name]
		r.ID = ""
		p.Repos = append(p.Repos, r)
	}
	for _, name := range names(s.Workspaces) {
		w, e := s.public(s.Workspaces[name])
		if e != nil {
			return p, e
		}
		w.ID = ""
		p.Workspaces = append(p.Workspaces, w)
	}
	return p, nil
}
func (s snapshot) internal(refs []Ref) ([]StoredRef, *Failure) {
	result := []StoredRef{}
	for _, ref := range refs {
		r, ok := s.Repos[ref.Repo]
		if !ok {
			return nil, missing("repo_config", ref.Repo)
		}
		result = append(result, StoredRef{r.ID, ref.Target})
	}
	return result, nil
}
func missing(entity, name string) *Failure {
	e := fail("CONFIG_NOT_FOUND", "Configuration not found")
	e.Details = map[string]any{"entity": entity, "name": name}
	return e
}
func uuid() (string, *Failure) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", ioFailure("Could not generate configuration ID")
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
