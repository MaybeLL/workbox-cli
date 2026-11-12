package workbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func gitlabHost(value string) (string, *Failure) {
	u, err := url.Parse("https://" + value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Host != value || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", invalid("Expected a GitLab hostname with optional port")
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && port != "443" {
		return net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host, nil
}
func connectionFile(root, kind, host string) string {
	return filepath.Join(root, "credentials", "gitlab-"+kind+"-"+base64.RawURLEncoding.EncodeToString([]byte(host)))
}
func saveConnection(root, kind, host, value string) *Failure {
	dir := filepath.Join(root, "credentials")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return ioFailure("Could not create credentials directory")
	}
	if e := privateDir(dir); e != nil {
		return e
	}
	_, e := atomicPrivateFile(connectionFile(root, kind, host), []byte(value), true)
	return e
}
func gitlabToken(raw string) (string, *Failure) {
	token := strings.TrimSpace(raw)
	if token == "" || strings.ContainsAny(token, " \t\r\n\x00") {
		return "", invalid("Invalid GitLab token")
	}
	return token, nil
}
func configureGitlab(c command) (any, *Failure) {
	root, e := settingsRoot()
	if e != nil {
		return nil, e
	}
	old, e := loadSettings(root)
	if e != nil {
		return nil, e
	}
	if old.value == nil {
		return nil, fail("NOT_INITIALIZED", "Run workbox init first")
	}
	host, e := gitlabHost(c.flags["host"])
	if e != nil {
		return nil, e
	}
	if c.action == "alias" {
		ssh, e := sshEndpoint(c.flags["ssh-host"])
		if e != nil {
			return nil, e
		}
		if _, e := checkedFile(connectionFile(root, "token", host), true); e != nil {
			return nil, invalid("Configure credentials for the HTTPS instance first")
		}
		if e := saveConnection(root, "alias", ssh, host); e != nil {
			return nil, e
		}
		return map[string]string{"host": host, "ssh_host": ssh}, nil
	}
	raw, e := checkedFile(c.flags["token-file"], false)
	if e != nil {
		return nil, e
	}
	token, e := gitlabToken(string(raw))
	if e != nil {
		return nil, e
	}
	if e := saveConnection(root, "token", host, token); e != nil {
		return nil, e
	}
	return map[string]string{"host": host}, nil
}
func sshEndpoint(value string) (string, *Failure) {
	u, err := url.Parse("ssh://" + value)
	if err != nil || u.User != nil || u.Host != value || u.Hostname() == "" {
		return "", invalid("Invalid SSH endpoint")
	}
	port := u.Port()
	if port == "" {
		port = "22"
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}
func (a *app) gitlabGet(ctx context.Context, host, endpoint string) ([]byte, *Failure) {
	data, _, e := a.gitlabPage(ctx, host, endpoint)
	return data, e
}
func (a *app) gitlabPage(ctx context.Context, host, endpoint string) ([]byte, http.Header, *Failure) {
	root, e := settingsRoot()
	if e != nil {
		return nil, nil, e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/api/v4"+endpoint, nil)
	if err != nil {
		return nil, nil, invalid("Invalid GitLab endpoint")
	}
	file := connectionFile(root, "token", host)
	if _, err := os.Lstat(file); err == nil {
		if e := privateDir(filepath.Dir(file)); e != nil {
			return nil, nil, e
		}
		token, e := checkedFile(file, true)
		if e != nil {
			return nil, nil, e
		}
		req.Header.Set("PRIVATE-TOKEN", string(token))
	} else if !os.IsNotExist(err) {
		return nil, nil, ioFailure("Could not inspect GitLab credentials")
	}
	client := http.Client{Timeout: 60 * time.Second, Transport: a.gitlabTransport, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fail("GITLAB_ERROR", "Could not contact GitLab host "+host)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e := fail("GITLAB_ERROR", fmt.Sprintf("GitLab %s returned HTTP %d; check the source and personal access", host, resp.StatusCode))
		e.Details["http_status"] = resp.StatusCode
		return nil, nil, e
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, ioFailure("Could not read GitLab response")
	}
	return b, resp.Header, nil
}

func apiHostForSSH(root, authority string) (string, *Failure) {
	endpoint, e := sshEndpoint(authority)
	if e != nil {
		return "", e
	}
	dir := filepath.Join(root, "credentials")
	if e := privateDir(dir); e != nil {
		return "", invalid("Configure the HTTPS instance and SSH alias for " + endpoint)
	}
	file := connectionFile(root, "alias", endpoint)
	if _, err := os.Lstat(file); err == nil {
		raw, e := checkedFile(file, true)
		if e != nil {
			return "", e
		}
		return gitlabHost(string(raw))
	} else if !os.IsNotExist(err) {
		return "", ioFailure("Could not inspect SSH alias")
	}
	ssh, _ := url.Parse("ssh://" + authority)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ioFailure("Could not inspect GitLab instances")
	}
	matches := []string{}
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), "gitlab-token-")
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(suffix)
		if err != nil {
			continue
		}
		u, err := url.Parse("https://" + string(raw))
		if err == nil && strings.EqualFold(u.Hostname(), ssh.Hostname()) {
			matches = append(matches, u.Host)
		}
	}
	if len(matches) != 1 {
		e := invalid("Set the GitLab instance using workbox auth gitlab alias set --ssh-host " + endpoint + " --host HOST")
		e.Details["ssh_host"] = endpoint
		return "", e
	}
	return matches[0], nil
}

type gitlabProject struct {
	client *app
	host   string
	api    string
}

func (a *app) gitlabProject(source string) (gitlabProject, *Failure) {
	if e := validateURL(source); e != nil {
		return gitlabProject{}, e
	}
	if !strings.Contains(source, "://") {
		authority, project, _ := strings.Cut(source, ":")
		source = "ssh://" + authority + "/" + strings.TrimPrefix(project, "/")
	}
	u, err := url.Parse(source)
	if err != nil {
		return gitlabProject{}, invalid("Invalid project URL")
	}
	project := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if !safeRelative(project) {
		return gitlabProject{}, invalid("Invalid project path")
	}
	var host string
	var e *Failure
	if u.Scheme == "ssh" {
		var root string
		root, e = settingsRoot()
		if e != nil {
			return gitlabProject{}, e
		}
		host, e = apiHostForSSH(root, u.Host)
		if e != nil {
			return gitlabProject{}, e
		}
	} else {
		host, e = gitlabHost(u.Host)
	}
	return gitlabProject{client: a, host: host, api: "/projects/" + url.PathEscape(project)}, e
}

func (p gitlabProject) fileAt(ctx context.Context, file, sha string) ([]byte, *Failure) {
	return p.client.gitlabGet(ctx, p.host, p.api+"/repository/files/"+url.PathEscape(file)+"/raw?ref="+url.QueryEscape(sha))
}

type gitlabCommit struct {
	ID string `json:"id"`
}

func (p gitlabProject) resolveBranch(ctx context.Context, branch string) (string, *Failure) {
	var reply struct {
		Commit gitlabCommit `json:"commit"`
	}
	return p.resolve(ctx, "branches/"+url.PathEscape(branch), &reply, &reply.Commit)
}
func (p gitlabProject) resolveRef(ctx context.Context, ref string) (string, *Failure) {
	var commit gitlabCommit
	return p.resolve(ctx, "commits/"+url.PathEscape(ref), &commit, &commit)
}
func (p gitlabProject) resolve(ctx context.Context, endpoint string, reply any, commit *gitlabCommit) (string, *Failure) {
	raw, e := p.client.gitlabGet(ctx, p.host, p.api+"/repository/"+endpoint)
	if e != nil {
		return "", e
	}
	if json.Unmarshal(raw, reply) != nil || !commitSHA.MatchString(commit.ID) {
		return "", fail("GITLAB_ERROR", "Invalid GitLab revision response")
	}
	return commit.ID, nil
}
