package workbox

import (
	"context"
	"os"
	"strings"
	"time"
)

func effectiveRef(repo Repo, ref *BaselineRef) BaselineRef {
	if ref != nil {
		return *ref
	}
	return BaselineRef{Type: "branch", Value: repo.Branch}
}

// Resolve before creating the destination. Historical commits cannot be checked
// with ls-remote; fetching into a disposable temporary repository verifies availability.
func (a *app) resolveBaseline(ctx context.Context, repo Repo, ref BaselineRef) (string, *Failure) {
	if e := validateBaselineRef(&ref); e != nil {
		return "", e
	}
	if ref.Type == "branch" {
		name := "refs/heads/" + ref.Value
		out, _, e := a.gitRunner.run(ctx, "", time.Minute, true, []int{0}, "ls-remote", "--heads", "--", repo.URL, name)
		if e != nil {
			return "", e
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) == 2 && fields[1] == name {
				if e := validateBaselineRef(&BaselineRef{Type: "commit", Value: fields[0]}); e != nil {
					return "", e
				}
				return fields[0], nil
			}
		}
		return "", fail("REMOTE_BRANCH_NOT_FOUND", "Selected remote branch does not exist")
	}
	dir, err := os.MkdirTemp("", "workbox-ref-")
	if err != nil {
		return "", ioFailure("Could not prepare commit verification")
	}
	defer os.RemoveAll(dir)
	if e := a.fetchCommit(ctx, dir, repo.URL, ref.Value); e != nil {
		return "", e
	}
	return ref.Value, nil
}

func (a *app) fetchCommit(ctx context.Context, dir, url, sha string) *Failure {
	args := []string{"init"}
	if len(sha) == 64 {
		args = append(args, "--object-format=sha256")
	}
	args = append(args, dir)
	if _, e := a.git(ctx, "", args...); e != nil {
		return e
	}
	if _, e := a.git(ctx, dir, "remote", "add", "origin", url); e != nil {
		return e
	}
	if _, _, e := a.gitRunner.run(ctx, dir, 1800*time.Second, true, []int{0}, "fetch", "--no-tags", "origin", sha); e != nil {
		return e
	}
	actual, e := a.git(ctx, dir, "rev-parse", "--verify", sha+"^{commit}")
	if e != nil {
		return e
	}
	if actual != sha {
		return fail("GIT_ERROR", "Selected SHA is not a commit object")
	}
	return nil
}
