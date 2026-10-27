package workbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var scpURL = regexp.MustCompile(`^(?:[A-Za-z0-9._-]+@)?[A-Za-z0-9][A-Za-z0-9.-]*:[^\\\s]+$`)
var drivePath = regexp.MustCompile(`^[A-Za-z]:`)

func invalid(message string) *Failure { return fail("CONFIG_INVALID", message) }
func validateName(name string) *Failure {
	if !slug.MatchString(name) {
		return invalid("Names must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	return nil
}
func validateURL(value string) *Failure {
	bad := invalid("Only remote HTTPS and SSH URLs without embedded credentials are supported")
	if value == "" || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return bad
	}
	u, err := url.Parse(value)
	if err == nil && (u.Scheme == "https" || u.Scheme == "ssh") {
		if u.Hostname() == "" || u.Path == "" || u.Path == "/" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
			return bad
		}
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword || u.Scheme == "https" {
				return bad
			}
		}
		return nil
	}
	if scpURL.MatchString(value) && !drivePath.MatchString(value) && !strings.Contains(value, "://") {
		return nil
	}
	return bad
}
func (a *app) validateRepo(ctx context.Context, r Repo) *Failure {
	if !utf8.ValidString(r.Summary) || strings.ContainsRune(r.Summary, 0) {
		return invalid("summary must be UTF-8 text without NUL")
	}
	if e := validateName(r.Name); e != nil {
		return e
	}
	if e := validateURL(r.URL); e != nil {
		return e
	}
	if strings.TrimSpace(r.Branch) == "" || strings.HasPrefix(r.Branch, "-") || r.Branch == "HEAD" {
		return invalid("A concrete, valid default branch is required")
	}
	_, rc, e := a.gitRunner.run(ctx, "", 60e9, false, []int{0, 1}, "check-ref-format", "refs/heads/"+r.Branch)
	if e != nil {
		return e
	}
	if rc != 0 {
		return invalid("Invalid Git branch name")
	}
	return nil
}
func validateRefs(refs []Ref) *Failure {
	seen := map[string]bool{}
	targets := []string{}
	fold := cases.Fold()
	for _, ref := range refs {
		if e := validateBaselineRef(ref.Ref); e != nil {
			return e
		}
		if e := validateName(ref.Repo); e != nil {
			return e
		}
		if seen[ref.Repo] {
			return invalid("A workspace cannot select a repository more than once")
		}
		seen[ref.Repo] = true
		path := ref.Target
		if path == "" || strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00") || drivePath.MatchString(path) {
			return invalid("target_dir must be a nonempty relative path using forward slashes")
		}
		for _, segment := range strings.Split(path, "/") {
			switch fold.String(segment) {
			case "", ".", "..", ".git", ".workbox":
				return invalid("target_dir contains a forbidden path segment")
			}
		}
		path = fold.String(path)
		if path == "agents.md" || strings.HasPrefix(path, "agents.md/") {
			return invalid("target_dir overlaps the workspace AGENTS.md instruction file")
		}
		for _, old := range targets {
			if path == old || strings.HasPrefix(path, old+"/") || strings.HasPrefix(old, path+"/") {
				return invalid("Repository target paths overlap (case-insensitive comparison)")
			}
		}
		targets = append(targets, path)
	}
	return nil
}
func validateWorkspace(w Workspace) *Failure {
	if e := validateName(w.Name); e != nil {
		return e
	}
	if e := validateRefs(w.Repos); e != nil {
		return e
	}
	if !utf8.ValidString(w.Script) {
		return invalid("init_script must be UTF-8 text")
	}
	if w.Script != "" {
		first := w.Script
		if end := strings.IndexAny(first, "\r\n"); end >= 0 {
			first = first[:end]
		}
		if !strings.HasPrefix(first, "#!") || strings.TrimSpace(first[2:]) == "" {
			return invalid("Nonempty init_script requires a first-line shebang with an interpreter")
		}
	}
	return nil
}
func readText(path string) ([]byte, *Failure) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ioFailure("Could not read input file")
	}
	if !utf8.Valid(data) {
		return nil, invalid("Input must be valid UTF-8")
	}
	return data, nil
}

// Check duplicates before typed decoding: encoding/json otherwise silently takes the last field.
func checkJSON(raw []byte) *Failure {
	if !utf8.Valid(raw) {
		return invalid("Input must be valid UTF-8 JSON")
	}
	// Do not silently replace unpaired JSON UTF-16 escapes with U+FFFD.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			continue
		}
		if raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return invalid("Invalid JSON Unicode escape")
		}
		r, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if err != nil {
			return invalid("Invalid JSON Unicode escape")
		}
		if r >= 0xD800 && r <= 0xDBFF {
			if i+12 > len(raw) || string(raw[i+6:i+8]) != `\u` {
				return invalid("Unpaired JSON Unicode surrogate")
			}
			next, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if err != nil || next < 0xDC00 || next > 0xDFFF {
				return invalid("Unpaired JSON Unicode surrogate")
			}
			i += 11
		} else if r >= 0xDC00 && r <= 0xDFFF {
			return invalid("Unpaired JSON Unicode surrogate")
		} else {
			i += 5
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 100 {
			return io.ErrUnexpectedEOF
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return io.ErrUnexpectedEOF
				}
				seen[s] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return io.ErrUnexpectedEOF
		}
		_, err = d.Token()
		return err
	}
	if err := visit(0); err != nil {
		return invalid("Input must be valid JSON without duplicate fields")
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid("Input must contain one JSON value")
	}
	return nil
}
func exact(raw []byte, out any, keys ...string) *Failure {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil || len(fields) != len(keys) {
		return invalid("Object has missing or unknown fields")
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid("Required fields must be present and non-null")
		}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return invalid("Invalid field type")
	}
	return nil
}
func parseRefs(raw []byte) ([]Ref, *Failure) {
	var items []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &items) != nil {
		return nil, invalid("repos must be an array")
	}
	refs := []Ref{}
	for _, item := range items {
		var ref Ref
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil {
			return nil, invalid("Invalid repository reference")
		}
		keys := []string{"repo", "target_dir"}
		if rawRef, ok := fields["ref"]; ok {
			keys = append(keys, "ref")
			var baseline BaselineRef
			if e := exact(rawRef, &baseline, "type", "value"); e != nil {
				return nil, e
			}
		}
		if e := exact(item, &ref, keys...); e != nil {
			return nil, e
		}
		refs = append(refs, ref)
	}
	if e := validateRefs(refs); e != nil {
		return nil, e
	}
	return refs, nil
}
func (a *app) parsePackage(ctx context.Context, raw []byte) (Package, *Failure) {
	p := Package{}
	if e := checkJSON(raw); e != nil {
		return p, e
	}
	var root struct {
		Version    json.RawMessage `json:"schema_version"`
		Repos      json.RawMessage `json:"repo_configs"`
		Workspaces json.RawMessage `json:"workspace_configs"`
		Teams      json.RawMessage `json:"team_configs"`
	}
	rootKeys := []string{"schema_version", "repo_configs", "workspace_configs"}
	var rootFields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &rootFields)
	if _, ok := rootFields["team_configs"]; ok {
		rootKeys = append(rootKeys, "team_configs")
	}
	if e := exact(raw, &root, rootKeys...); e != nil {
		return p, e
	}
	if json.Unmarshal(root.Version, &p.Version) != nil || p.Version != 1 {
		return p, fail("SCHEMA_UNSUPPORTED", "Only configuration package version 1 is supported")
	}
	var rows []json.RawMessage
	teamKeys := map[string]bool{}
	if root.Teams != nil {
		if json.Unmarshal(root.Teams, &rows) != nil {
			return p, invalid("team_configs must be an array")
		}
		for _, row := range rows {
			var t Team
			keys := []string{"key", "name"}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(row, &fields)
			if _, ok := fields["summary"]; ok {
				keys = append(keys, "summary")
			}
			if e := exact(row, &t, keys...); e != nil {
				return p, e
			}
			if e := validateTeam(t); e != nil {
				return p, e
			}
			if teamKeys[t.Key] {
				return p, invalid("Duplicate team key")
			}
			teamKeys[t.Key] = true
			p.Teams = append(p.Teams, t)
		}
	}
	if json.Unmarshal(root.Repos, &rows) != nil {
		return p, invalid("repo_configs must be an array")
	}
	p.Repos = []Repo{}
	available := map[string]bool{}
	for _, row := range rows {
		var r Repo
		keys := []string{"name", "source_url", "default_branch"}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(row, &fields)
		if _, ok := fields["summary"]; ok {
			keys = append(keys, "summary")
		}
		if _, ok := fields["teams"]; ok {
			keys = append(keys, "teams")
		}
		if e := exact(row, &r, keys...); e != nil {
			return p, e
		}
		var e *Failure
		r.Teams, e = normalizeTeams(r.Teams)
		if e != nil {
			return p, e
		}
		for _, key := range r.Teams {
			if !teamKeys[key] {
				return p, invalid("Package must include every referenced team")
			}
		}
		if e := a.validateRepo(ctx, r); e != nil {
			return p, e
		}
		if available[r.Name] {
			return p, invalid("Duplicate repository configuration name")
		}
		available[r.Name] = true
		p.Repos = append(p.Repos, r)
	}
	if json.Unmarshal(root.Workspaces, &rows) != nil {
		return p, invalid("workspace_configs must be an array")
	}
	p.Workspaces = []Workspace{}
	seen := map[string]bool{}
	for _, row := range rows {
		var w Workspace
		if e := exact(row, &w, "name", "repos", "init_script"); e != nil {
			return p, e
		}
		var values map[string]json.RawMessage
		_ = json.Unmarshal(row, &values)
		refs, e := parseRefs(values["repos"])
		if e != nil {
			return p, e
		}
		w.Repos = refs
		if e := validateWorkspace(w); e != nil {
			return p, e
		}
		if seen[w.Name] {
			return p, invalid("Duplicate workspace configuration name")
		}
		seen[w.Name] = true
		for _, ref := range w.Repos {
			if !available[ref.Repo] {
				return p, invalid("Configuration package must contain every referenced repository")
			}
		}
		p.Workspaces = append(p.Workspaces, w)
	}
	return p, nil
}

// Validate without consulting the current directory or expanding revision expressions.
func validateBaselineRef(ref *BaselineRef) *Failure {
	if ref == nil {
		return nil
	}
	switch ref.Type {
	case "commit":
		if !regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(ref.Value) {
			return invalid("commit ref requires a full lowercase hexadecimal SHA")
		}
	case "branch":
		v := ref.Value
		if v == "" || v == "HEAD" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, " ~^:?*[\\") || strings.Contains(v, "..") || strings.Contains(v, "@{") || strings.HasSuffix(v, ".") || strings.ContainsFunc(v, unicode.IsControl) {
			return invalid("Invalid branch ref")
		}
		for _, part := range strings.Split(v, "/") {
			if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
				return invalid("Invalid branch ref")
			}
		}
	default:
		return invalid("ref type must be branch or commit")
	}
	return nil
}
