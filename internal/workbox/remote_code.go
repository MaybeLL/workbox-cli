package workbox

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
)

type codeHit struct {
	Path  string `json:"path"`
	Ref   string `json:"ref"`
	Start int    `json:"startline"`
	Data  string `json:"data"`
}
type searchResult struct {
	Repo     string    `json:"repo"`
	Ref      string    `json:"ref"`
	Coverage string    `json:"coverage"`
	Hits     []codeHit `json:"hits"`
	Error    *Failure  `json:"error,omitempty"`
}

func (a *app) remoteCode(ctx context.Context, c command) (any, *Failure) {
	dir, err := os.OpenRoot(c.flags["path"])
	if err != nil {
		return nil, ioFailure("Cannot open Workbox directory")
	}
	defer dir.Close()
	raw, err := dir.ReadFile(".workbox/repos.json")
	var manifest []workspaceRepo
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest == nil {
		return nil, invalid("Invalid or missing .workbox/repos.json")
	}
	selected := c.repos
	if c.entity != "search" {
		selected = []string{c.flags["repo"]}
	}
	byName := map[string]workspaceRepo{}
	for _, r := range manifest {
		if _, duplicate := byName[r.Name]; duplicate {
			return nil, invalid("Duplicate manifest repository")
		}
		byName[r.Name] = r
		if len(c.repos) == 0 && c.entity == "search" {
			selected = append(selected, r.Name)
		}
	}
	for _, name := range selected {
		if _, ok := byName[name]; !ok {
			return nil, missing("instance_repo", name)
		}
	}
	for name := range c.overrides {
		if !slices.Contains(selected, name) {
			return nil, invalid("Version override outside selected repositories: " + name)
		}
	}
	var partial bool
	results := []searchResult{}
	for _, name := range selected {
		r := byName[name]
		selection := BaselineRef{Type: "branch", Value: r.DefaultBranch}
		if r.Ref != nil {
			selection = *r.Ref
		}
		ref := selection.Value
		if v := c.overrides[name]; v != "" {
			ref = v
		}
		project, e := a.gitlabProject(r.URL)
		if c.entity != "search" {
			if e != nil {
				e.Details["repo"] = name
				return nil, e
			}
			if c.entity == "read" && !safeRelative(c.flags["file"]) {
				return nil, invalid("Expected a file path")
			}
			override := c.flags["ref"]
			if override != "" {
				ref = override
			}
			sha, e := project.resolveCodeVersion(ctx, selection, override, c.entity == "resolve-ref")
			if e != nil {
				return nil, e
			}
			if c.entity == "resolve-ref" {
				return map[string]string{"repo": name, "ref": ref, "commit": sha}, nil
			}
			return project.readCode(ctx, name, sha, c.flags["file"], c.flags["lines"])
		}
		row := searchResult{Repo: name, Ref: ref, Coverage: "uncovered", Hits: []codeHit{}}
		if e == nil {
			e = project.search(ctx, c.flags["query"], &row)
		}
		row.Error = e
		if e != nil {
			partial = true
		}
		results = append(results, row)
	}
	data := map[string]any{"repos": results}
	if partial {
		e := fail("SEARCH_PARTIAL", "Some repository searches are incomplete")
		e.Data = data
		return nil, e
	}
	return data, nil
}

func (p gitlabProject) search(ctx context.Context, term string, row *searchResult) *Failure {
	if commitSHA.MatchString(row.Ref) {
		return fail("REF_UNSUPPORTED", "GitLab code search cannot verify a commit SHA; read a known file at this revision")
	}
	for page := 1; ; page++ {
		q := url.Values{"scope": {"blobs"}, "ref": {row.Ref}, "search": {term}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		raw, headers, e := p.client.gitlabPage(ctx, p.host, p.api+"/search?"+q.Encode())
		if e != nil {
			return e
		}
		var hits []codeHit
		if json.Unmarshal(raw, &hits) != nil || hits == nil {
			return fail("GITLAB_ERROR", "Invalid search response")
		}
		for _, hit := range hits {
			if hit.Ref != row.Ref {
				return fail("REF_UNVERIFIED", "GitLab returned a different or unknown search ref")
			}
			row.Hits = append(row.Hits, hit)
		}
		row.Coverage = "partial"
		next := headers.Get("X-Next-Page")
		if next != "" {
			n, err := strconv.Atoi(next)
			if err != nil || n != page+1 {
				return fail("GITLAB_ERROR", "Invalid pagination")
			}
			continue
		}
		if len(hits) < 100 {
			break
		}
	}
	// An empty index response carries no ref evidence.
	if len(row.Hits) == 0 {
		row.Coverage = "unknown"
		return fail("COVERAGE_UNKNOWN", "No hits; the index response does not establish version coverage")
	}
	row.Coverage = "indexed_results"
	return nil
}

// A declared branch can only be resolved by the branches API. For a different,
// explicitly supplied ref, read retains GitLab's generic ref lookup, while a
// tip check requires a live branch unless the caller supplied a pinned SHA.
func (p gitlabProject) resolveCodeVersion(ctx context.Context, selection BaselineRef, override string, checkTip bool) (string, *Failure) {
	if override != "" && override != selection.Value {
		if checkTip && !commitSHA.MatchString(override) {
			return p.resolveBranch(ctx, override)
		}
		return p.resolveRef(ctx, override)
	}
	if selection.Type == "branch" {
		return p.resolveBranch(ctx, selection.Value)
	}
	return p.resolveRef(ctx, selection.Value)
}

func (p gitlabProject) readCode(ctx context.Context, name, sha, file, lines string) (any, *Failure) {
	b, e := p.fileAt(ctx, file, sha)
	if e != nil {
		return nil, e
	}
	parts := strings.SplitAfter(string(b), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	start, end := 1, len(parts)
	if lines != "" {
		left, right, ok := strings.Cut(lines, "-")
		var err error
		start, err = strconv.Atoi(left)
		if !ok || err != nil || start < 1 {
			return nil, invalid("Expected a 1-based inclusive line range")
		}
		end, err = strconv.Atoi(right)
		if err != nil || end < start || end > len(parts) {
			return nil, invalid("Line range exceeds file")
		}
	}
	return map[string]any{"repo": name, "commit": sha, "path": file, "start_line": start, "end_line": end, "data": strings.Join(parts[start-1:end], "")}, nil
}
