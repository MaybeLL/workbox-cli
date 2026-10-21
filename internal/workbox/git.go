package workbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type gitRunner interface {
	run(context.Context, string, time.Duration, bool, []int, ...string) (string, int, *Failure)
}
type gitCLI struct{}

// Never forward raw Git diagnostics: credential helpers and transports may print secrets.
func (gitCLI) run(ctx context.Context, cwd string, timeout time.Duration, remote bool, allowed []int, args ...string) (string, int, *Failure) {
	child, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := append([]string{"-c", "credential.interactive=false", "-c", "core.askPass="}, args...)
	cmd := exec.CommandContext(child, "git", command...)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	ssh := os.Getenv("GIT_SSH_COMMAND")
	if ssh == "" {
		ssh = os.Getenv("GIT_SSH")
	}
	if ssh == "" {
		ssh = "ssh"
	}
	for key, val := range map[string]string{"GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "Never", "GIT_OPTIONAL_LOCKS": "0", "GIT_ASKPASS": "", "SSH_ASKPASS": "", "SSH_ASKPASS_REQUIRE": "never", "GIT_SSH_COMMAND": ssh + " -oBatchMode=yes -oStrictHostKeyChecking=yes"} {
		cmd.Env = setEnv(cmd.Env, key, val)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	var output bytes.Buffer
	cmd.Stdout = &output
	err := cmd.Run()
	rc := 0
	if ctx.Err() != nil {
		return "", -1, fail("INTERRUPTED", "Operation interrupted")
	}
	if child.Err() != nil {
		e := fail("TIMEOUT", "Git operation timed out")
		e.Details["timeout_seconds"] = timeout.Seconds()
		return "", -1, e
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return "", -1, fail("GIT_ERROR", "Could not start Git")
		}
		rc = exit.ExitCode()
	}
	for _, code := range allowed {
		if code == rc {
			return strings.TrimSuffix(output.String(), "\n"), rc, nil
		}
	}
	e := fail("GIT_ERROR", "Git operation failed")
	if remote {
		e = fail("REMOTE_UNVERIFIABLE", "Remote access could not be verified; check connectivity and Git credentials")
	}
	e.Details["returncode"] = rc
	return "", rc, e
}
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+val)
}
func (a *app) git(ctx context.Context, cwd string, args ...string) (string, *Failure) {
	out, _, e := a.gitRunner.run(ctx, cwd, 60*time.Second, false, []int{0}, args...)
	return out, e
}
func (a *app) verifyRemote(ctx context.Context, repo Repo) *Failure {
	ref := "refs/heads/" + repo.Branch
	out, _, e := a.gitRunner.run(ctx, "", 60*time.Second, true, []int{0}, "ls-remote", "--heads", "--", repo.URL, ref)
	if e != nil {
		return e
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) == 2 && parts[1] == ref {
			return nil
		}
	}
	e = fail("REMOTE_BRANCH_NOT_FOUND", "Remote branch does not exist")
	e.Details = map[string]any{"repo": repo.Name, "default_branch": repo.Branch}
	return e
}
