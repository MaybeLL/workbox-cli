package workbox

import (
	"context"
	"encoding/json"
	"golang.org/x/text/cases"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

type Document struct {
	Project string      `json:"project_url"`
	Ref     BaselineRef `json:"ref"`
	Source  string      `json:"source_path"`
	Target  string      `json:"target_path"`
}

func parseDocuments(raw []byte) ([]Document, *Failure) {
	if e := checkJSON(raw); e != nil {
		return nil, e
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, invalid("docs must be an array")
	}
	var docs []Document
	for _, item := range items {
		var doc Document
		if e := exact(item, &doc, "project_url", "ref", "source_path", "target_path"); e != nil {
			return nil, e
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(item, &fields)
		if e := exact(fields["ref"], &doc.Ref, "type", "value"); e != nil {
			return nil, e
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func safeRelative(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\\x00") && !drivePath.MatchString(name)
}
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func validateDocuments(w Workspace) *Failure {
	targets := []string{".workbox", ".agents/skills"}
	fold := cases.Fold()
	for _, r := range w.Repos {
		targets = append(targets, fold.String(r.Target))
	}
	for _, d := range w.Docs {
		u, err := url.Parse(d.Project)
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || !safeRelative(strings.TrimPrefix(u.Path, "/")) || u.Hostname() == "" {
			return invalid("Invalid HTTPS GitLab project URL")
		}
		if e := validateBaselineRef(&d.Ref); e != nil {
			return e
		}
		if !safeRelative(d.Source) || !safeRelative(d.Target) || !strings.EqualFold(path.Ext(d.Target), ".md") {
			return invalid("Invalid Markdown source or destination path")
		}
		t := fold.String(d.Target)
		for _, part := range strings.Split(t, "/") {
			if part == ".git" || part == ".workbox" {
				return invalid("Reserved document destination: " + d.Target)
			}
		}
		for _, other := range targets {
			if overlaps(t, other) {
				return invalid("Document destination " + d.Target + " overlaps " + other)
			}
		}
		targets = append(targets, t)
	}
	return nil
}

// Resolve branches once for all documents from a project in this creation.
func (a *app) prepareDocuments(ctx context.Context, tree *ownedTree, docs []Document) *Failure {
	type selection struct {
		project gitlabProject
		branch  string
	}
	revisions := map[selection]string{}
	for _, d := range docs {
		project, e := a.gitlabProject(d.Project)
		if e != nil {
			return documentFailure(e, d)
		}
		sha := d.Ref.Value
		if d.Ref.Type == "branch" {
			key := selection{project, d.Ref.Value}
			sha = revisions[key]
			if sha == "" {
				sha, e = project.resolveBranch(ctx, d.Ref.Value)
				if e != nil {
					return documentFailure(e, d)
				}
				revisions[key] = sha
			}
		}
		b, e := project.fileAt(ctx, d.Source, sha)
		if e != nil {
			return documentFailure(e, d)
		}
		dir := ""
		for _, part := range strings.Split(path.Dir(d.Target), "/") {
			if part == "." {
				continue
			}
			dir = path.Join(dir, part)
			if e := tree.mkdir(dir); e != nil {
				return documentFailure(e, d)
			}
		}
		if e := tree.writeFile(d.Target, b); e != nil {
			return documentFailure(e, d)
		}
	}
	return nil
}
func documentFailure(e *Failure, d Document) *Failure {
	e.Details["target_path"] = d.Target
	e.Details["project_url"] = d.Project
	e.Details["source_path"] = d.Source
	e.Details["ref"] = d.Ref
	return e
}
