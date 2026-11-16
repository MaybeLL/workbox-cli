package workbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
)

type app struct {
	publishInstance func(*os.Root, string, string) error
	gitlabTransport http.RoundTripper
	store           configStore
	gitRunner       gitRunner
}
type command struct {
	repos                []string
	overrides            map[string]string
	entity, action, name string
	flags                map[string]string
}
type envelope struct {
	OK    bool     `json:"ok"`
	Data  any      `json:"data"`
	Error *Failure `json:"error"`
}

const usage = `Usage: workbox COMMAND [--format text|json]

  init [--backend sqlite|mysql] [--mysql-dsn-file FILE]
       [--install-skill | --skip-skill] [--skill-path DIR]
       [--gitlab-host HOST --gitlab-token-file FILE]
       [--copy-config | --no-copy-config]
  auth gitlab set --host HOST --token-file FILE
  auth gitlab alias set --ssh-host SSH_HOST --host HOST
  repo-config add NAME --url URL --default-branch BRANCH [--summary TEXT] [--teams KEY1,KEY2]
  repo-config list [--team KEY]
  repo-config show NAME
  repo-config update NAME [--url URL] [--default-branch BRANCH] [--summary TEXT] [--teams KEY1,KEY2]
  repo-config remove NAME
  team-config add KEY --name NAME [--summary TEXT]
  team-config list
  team-config show KEY
  team-config update KEY [--name NAME] [--summary TEXT]
  team-config remove KEY
  repo-config add-team NAME --team KEY
  repo-config remove-team NAME --team KEY
  workbox-config add NAME [--docs-file FILE] [--repos-file FILE] [--init-script-file FILE]
  workbox-config list
  workbox-config show NAME
  workbox-config update NAME --revision REV [--docs-file FILE] [--repos-file FILE] [--init-script-file FILE]
  workbox-config remove NAME
  config export --output FILE
  config import --input FILE
  plan --config NAME --path DIR
  create --config NAME --path DIR
  add-repo --path DIR (--url URL [--name NAME] | --repo NAME) [--target-dir REL] [--branch BRANCH | --commit SHA]
  resolve-ref --path DIR --repo NAME [--ref REF]
  search --path DIR --query TERM [--repo NAME]... [--ref NAME=REF]...
  read --path DIR --repo NAME --file PATH [--ref REF] [--lines START-END]
  status --path DIR
  skill-config add --git-url URL [--subdir DIR] [--branch NAME | --commit SHA]
  skill-config add --installer npx-skills URL --skill NAME
  skill-config update NAME (same source options as add)
  skill-config list | show NAME | remove NAME
  workbox-config add-skill|remove-skill NAME SKILL --revision REV
  skill install [--path SKILLS_DIRECTORY]
  uninstall [--dry-run] [--skill-path SKILLS_DIRECTORY]
  --help
  --version

Every command supports --format json; text is the default.
Scripts are stored and returned to the caller, never executed.
Create writes .workbox/repos.json and the configured documents and Skills.
Plan and create do not check out business repositories. target_dir is the requested checkout location.
Skill install copies the bundled skill to ~/.agents/skills/workbox by default.
--path selects a different skills parent directory. Verified unmodified older installations are upgraded; customized content is preserved.
Uninstall removes the CLI and unmodified companion skills; configuration and Workbox instances are preserved.
--dry-run previews uninstall. --skill-path locates a custom skill installed by an older version.

Run workbox init before configuration, export/import, or plan/create commands.
Interactive init requires a terminal. Scripts must specify --backend and exactly one Skill choice.
Settings: settings.json under WORKBOX_CONFIG_HOME, otherwise ${XDG_CONFIG_HOME:-~/.config}/workbox.
Relative config roots resolve from each invocation's working directory; prefer absolute paths.
SQLite uses config.db in that root; MySQL uses an existing dedicated database.
Environment overrides: WORKBOX_DB and WORKBOX_MYSQL_DSN or WORKBOX_MYSQL_DSN_FILE.
Overrides require valid initialization settings. init can copy configurations while retaining the source.
`

func outputFormat(args []string) ([]string, string, *Failure) {
	clean := []string{}
	format := "text"
	seenFormat := false
	// Preserve JSON error output even when another argument is invalid.
	for i, arg := range args {
		if arg == "--format=json" || (arg == "--format" && i+1 < len(args) && args[i+1] == "json") {
			format = "json"
		}
	}
	errorFormat := format
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--format" {
			if seenFormat {
				return clean, errorFormat, fail("INVALID_ARGUMENTS", "--format must not be repeated")
			}
			seenFormat = true
			i++
			if i == len(args) {
				return clean, errorFormat, fail("INVALID_ARGUMENTS", "--format requires text or json")
			}
			format = args[i]
		} else if strings.HasPrefix(arg, "--format=") {
			if seenFormat {
				return clean, errorFormat, fail("INVALID_ARGUMENTS", "--format must not be repeated")
			}
			seenFormat = true
			format = strings.TrimPrefix(arg, "--format=")
		} else {
			clean = append(clean, arg)
		}
	}
	if format != "text" && format != "json" {
		return clean, errorFormat, fail("INVALID_ARGUMENTS", "--format requires text or json")
	}
	return clean, format, nil
}
func parse(args []string) (command, *Failure) {
	c := command{flags: map[string]string{}}
	bad := fail("INVALID_ARGUMENTS", "Invalid command arguments; use --help for syntax")
	if len(args) == 0 {
		return c, bad
	}
	// Accept the former command spellings for existing scripts.
	if args[0] == "workspace" && len(args) >= 2 {
		switch args[1] {
		case "plan", "create", "status":
			args = args[1:]
		default:
			return c, bad
		}
	}
	c.entity = args[0]
	if c.entity == "workspace-config" {
		c.entity = "workbox-config"
	}
	start := 1
	switch c.entity {
	case "uninstall", "init", "plan", "create", "status", "search", "read", "add-repo", "resolve-ref":
	default:
		if len(args) < 2 {
			return c, bad
		}
		c.action = args[1]
		start = 2
	}
	allowed := map[string]bool{}
	required := []string{}
	needsName := false
	switch c.entity {
	case "auth":
		if c.action != "gitlab" || len(args) < 3 {
			return c, bad
		}
		c.action = args[2]
		start = 3
		switch c.action {
		case "set":
			required = []string{"host", "token-file"}
		case "alias":
			if len(args) < 4 || args[3] != "set" {
				return c, bad
			}
			start = 4
			required = []string{"host", "ssh-host"}
		default:
			return c, bad
		}
		for _, key := range required {
			allowed[key] = true
		}
	case "init":
		for _, name := range []string{"gitlab-host", "gitlab-token-file", "backend", "mysql-dsn-file", "install-skill", "skip-skill", "skill-path", "copy-config", "no-copy-config"} {
			allowed[name] = true
		}
	case "uninstall":
		allowed["dry-run"] = true
		allowed["skill-path"] = true
	case "skill-config":
		switch c.action {
		case "list":
		case "show", "remove":
			needsName = true
		case "add", "update":
			for _, k := range []string{"git-url", "subdir", "branch", "commit", "installer", "skill"} {
				allowed[k] = true
			}
		default:
			return c, bad
		}
	case "repo-config", "workbox-config", "team-config":
		switch c.action {
		case "add", "update":
			if c.entity == "repo-config" {
				allowed["url"] = true
				allowed["default-branch"] = true
				allowed["summary"] = true
				allowed["teams"] = true
				if c.action == "add" {
					required = []string{"url", "default-branch"}
				}
			} else if c.entity == "team-config" {
				allowed["name"] = true
				allowed["summary"] = true
				if c.action == "add" {
					required = []string{"name"}
				}
			} else {
				allowed["docs-file"] = true
				allowed["repos-file"] = true
				allowed["init-script-file"] = true
			}
			needsName = true
		case "show", "remove":
			needsName = true
		case "list":
			if c.entity == "repo-config" {
				allowed["team"] = true
			}
		case "add-skill", "remove-skill":
			if c.entity != "workbox-config" {
				return c, bad
			}
		case "add-team", "remove-team":
			if c.entity != "repo-config" {
				return c, bad
			}
			needsName = true
			allowed["team"] = true
			required = []string{"team"}
		default:
			return c, bad
		}
	case "config":
		switch c.action {
		case "export":
			required = []string{"output"}
			allowed["output"] = true
		case "import":
			required = []string{"input"}
			allowed["input"] = true
		default:
			return c, bad
		}
	case "plan", "create":
		required = []string{"config", "path"}
		allowed["config"] = true
		allowed["path"] = true
	case "add-repo":
		required = []string{"path"}
		for _, k := range []string{"path", "url", "repo", "name", "target-dir", "branch", "commit"} {
			allowed[k] = true
		}
	case "resolve-ref":
		required = []string{"path", "repo"}
		for _, k := range []string{"path", "repo", "ref"} {
			allowed[k] = true
		}
	case "search", "read":
		allowed["path"] = true
		allowed["repo"] = true
		allowed["ref"] = true
		if c.entity == "search" {
			required = []string{"path", "query"}
			allowed["query"] = true
		} else {
			required = []string{"path", "repo", "file"}
			allowed["file"] = true
			allowed["lines"] = true
		}
	case "status":
		required = []string{"path"}
		allowed["path"] = true
	case "skill":
		if c.action != "install" {
			return c, bad
		}
		allowed["path"] = true
	default:
		return c, bad
	}
	guarded := c.entity == "workbox-config" && (c.action == "update" || c.action == "add-skill" || c.action == "remove-skill")
	if guarded {
		allowed["revision"] = true
	}
	positional := []string{}
	literal := false
	for i := start; i < len(args); i++ {
		arg := args[i]
		if arg == "--" && !literal {
			literal = true
			continue
		}
		if strings.HasPrefix(arg, "-") && !literal {
			if !strings.HasPrefix(arg, "--") {
				return c, bad
			}
			flag, val, hasValue := strings.Cut(arg[2:], "=")
			if !allowed[flag] {
				return c, bad
			}
			if _, ok := c.flags[flag]; ok {
				return c, bad
			}
			if c.entity == "init" && (flag == "install-skill" || flag == "skip-skill" || flag == "copy-config" || flag == "no-copy-config") {
				if hasValue {
					return c, bad
				}
				c.flags[flag] = "true"
				continue
			}
			if c.entity == "uninstall" && flag == "dry-run" {
				if !hasValue {
					val = "true"
				}
				if val != "true" && val != "false" {
					return c, bad
				}
				c.flags[flag] = val
				continue
			}
			if !hasValue {
				i++
				if i == len(args) {
					return c, bad
				}
				val = args[i]
			}
			if c.entity == "search" && flag == "repo" {
				if validateName(val) != nil || slices.Contains(c.repos, val) {
					return c, bad
				}
				c.repos = append(c.repos, val)
				continue
			}
			if c.entity == "search" && flag == "ref" {
				name, ref, ok := strings.Cut(val, "=")
				if !ok || ref == "" || validateName(name) != nil {
					return c, bad
				}
				if c.overrides == nil {
					c.overrides = map[string]string{}
				}
				if _, ok := c.overrides[name]; ok {
					return c, bad
				}
				c.overrides[name] = ref
				continue
			}
			c.flags[flag] = val
		} else {
			positional = append(positional, arg)
		}
	}
	if c.entity == "add-repo" {
		if (c.flags["url"] == "") == (c.flags["repo"] == "") || (c.flags["repo"] != "" && c.flags["name"] != "") || (c.flags["branch"] != "" && c.flags["commit"] != "") {
			return c, bad
		}
	}
	if c.entity == "search" && strings.TrimSpace(c.flags["query"]) == "" {
		return c, bad
	}
	if c.entity == "workbox-config" && (c.action == "add-skill" || c.action == "remove-skill") {
		if len(positional) != 2 {
			return c, bad
		}
		c.name = positional[0]
		c.flags["skill"] = positional[1]
		positional = nil
	}
	if c.entity == "skill-config" && (c.action == "add" || c.action == "update") {
		if c.action == "update" {
			if len(positional) < 1 {
				return c, bad
			}
			c.name = positional[0]
			positional = positional[1:]
		}
		if _, ok := c.flags["installer"]; ok {
			if c.flags["installer"] != "npx-skills" || len(positional) != 1 || len(c.flags) != 2 || c.flags["skill"] == "" {
				return c, bad
			}
			c.flags["source"] = positional[0]
			positional = nil
		} else if c.flags["git-url"] == "" || c.flags["skill"] != "" {
			return c, bad
		}
	}
	if guarded && c.flags["revision"] == "" {
		return c, fail("INVALID_ARGUMENTS", "Supply --revision from workbox-config show")
	}
	if guarded && c.action == "update" && len(c.flags) == 1 {
		return c, bad
	}
	if needsName {
		if len(positional) != 1 {
			return c, bad
		}
		c.name = positional[0]
	} else if len(positional) != 0 {
		return c, bad
	}
	for _, flag := range required {
		if _, ok := c.flags[flag]; !ok {
			return c, bad
		}
	}
	if c.entity == "init" && len(c.flags) > 0 {
		if _, e := initFlags(c); e != nil {
			return c, e
		}
	}
	if (c.entity == "repo-config" || c.entity == "workbox-config" || c.entity == "team-config") && c.action == "update" && len(c.flags) == 0 {
		return c, bad
	}
	return c, nil
}
func (a *app) execute(ctx context.Context, c command) (any, *Failure) {
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
	}
	if c.entity == "auth" {
		return configureGitlab(c)
	}
	if c.entity == "skill" {
		return installSkill(ctx, c.flags)
	}
	if c.entity == "uninstall" {
		binary, err := os.Executable()
		if err != nil {
			return nil, ioFailure("Could not locate the running executable")
		}
		return uninstall(ctx, binary, os.Args[0], c.flags)
	}
	if c.entity == "add-repo" {
		return a.appendInstanceRepo(ctx, c)
	}
	if c.entity == "search" || c.entity == "read" || c.entity == "resolve-ref" {
		return a.remoteCode(ctx, c)
	}
	if c.entity == "status" {
		return a.status(ctx, c.flags["path"])
	}
	if a.store == nil {
		backend, e := configuredStore()
		if e != nil {
			return nil, e
		}
		a.store = backend
	}
	switch c.entity {
	case "skill-config":
		return a.skillCatalog(ctx, c)
	case "repo-config", "workbox-config", "team-config":
		if c.entity == "workbox-config" && (c.action == "update" || c.action == "add-skill" || c.action == "remove-skill") {
			change, e := parseWorkspaceChange(c)
			if e != nil {
				return nil, e
			}
			value, e := updateWorkspace(ctx, a.store, c.name, c.flags["revision"], change)
			if e != nil {
				return nil, e
			}
			return map[string]any{"workspace_config": value}, nil
		}
		if c.entity == "team-config" {
			return a.teamCRUD(ctx, c)
		}
		if c.action == "add-team" || c.action == "remove-team" {
			return a.repoTeam(ctx, c)
		}
		return a.crud(ctx, c)
	case "config":
		if c.action == "export" {
			return a.export(ctx, c.flags["output"])
		}
		return a.importFile(ctx, c.flags["input"])
	default:
		return a.prepare(ctx, c.flags["config"], c.flags["path"], c.entity == "create")
	}
}

// Run provides one envelope on stdout, including argument errors and partial failures.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	return (&app{gitRunner: gitCLI{}}).run(ctx, args, stdout, stderr, version)
}
func (a *app) run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	clean, format, e := outputFormat(args)
	var data any
	if e == nil {
		help, ver := false, false
		for _, arg := range clean {
			help = help || arg == "--help" || arg == "-h"
			ver = ver || arg == "--version"
		}
		if help {
			data = map[string]any{"usage": usage, "commands": []string{"init", "repo-config add/list/show/update/remove", "team-config add/list/show/update/remove", "workbox-config add/list/show/update/remove/add-skill/remove-skill", "config export/import", "plan", "create", "status", "search", "read", "add-repo", "resolve-ref", "auth gitlab set/alias set", "skill-config add/list/show/update/remove", "skill install", "uninstall"}}
		} else if ver && len(clean) == 1 {
			data = map[string]string{"program": "workbox", "version": version}
		} else {
			var c command
			c, e = parse(clean)
			if e == nil {
				if c.entity == "init" {
					data, e = a.initialize(ctx, c, format, stderr)
				} else {
					data, e = a.execute(ctx, c)
				}
			}
		}
	}
	_, published := data.(publishedInstance)
	if !published && ctx.Err() != nil && (e == nil || e.Code != "INTERRUPTED") {
		interrupted := fail("INTERRUPTED", "Operation interrupted")
		interrupted.Data = data
		if e != nil {
			interrupted.Data = e.Data
		}
		if partial, ok := interrupted.Data.(map[string]any); ok && partial["state"] != nil {
			if partial["settings"] == nil {
				partial["state"] = "failed"
			} else if partial["state"] == "initialized" {
				partial["state"] = "partial"
			}
		}
		e = interrupted
	}
	result := envelope{OK: e == nil, Data: data, Error: e}
	code := 0
	if e != nil {
		result.Data = e.Data
		code = e.ExitCode()
	}
	if format == "json" {
		enc := json.NewEncoder(stdout)
		if err := enc.Encode(result); err != nil {
			fmt.Fprintln(stderr, "Could not write command output")
			return 1
		}
	} else if len(clean) > 0 && clean[0] == "init" {
		renderInitOutcome(stdout, result.Data, e)
	} else if result.OK && data != nil {
		if object, ok := data.(map[string]any); ok && object["usage"] != nil {
			fmt.Fprint(stdout, object["usage"])
		} else {
			human(stdout, data, 0)
		}
	} else {
		if result.Data != nil {
			human(stdout, result.Data, 0)
		}
		if e != nil {
			human(stdout, map[string]any{"error": e}, 0)
		}
	}
	return code
}
func human(w io.Writer, value any, indent int) {
	raw, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(raw, &normalized)
	render(w, normalized, indent)
}
func render(w io.Writer, value any, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case map[string]any:
		keys := []string{}
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			item := v[key]
			if key == "branch" && item == nil && v["commit"] != nil && v["error"] == nil {
				fmt.Fprintln(w, pad+"branch: detached HEAD")
				continue
			}
			switch x := item.(type) {
			case map[string]any:
				if len(x) > 0 {
					fmt.Fprintln(w, pad+key+":")
					render(w, x, indent+2)
				} else {
					fmt.Fprintln(w, pad+key+": {}")
				}
			case []any:
				if len(x) > 0 {
					fmt.Fprintln(w, pad+key+":")
					render(w, x, indent+2)
				} else {
					fmt.Fprintln(w, pad+key+": []")
				}
			case string:
				if strings.Contains(x, "\n") {
					fmt.Fprintln(w, pad+key+": |")
					for _, line := range strings.Split(x, "\n") {
						fmt.Fprintln(w, pad+"  "+line)
					}
				} else {
					fmt.Fprintln(w, pad+key+": "+x)
				}
			default:
				raw, _ := json.Marshal(item)
				fmt.Fprintln(w, pad+key+": "+string(raw))
			}
		}
	case []any:
		for _, item := range v {
			fmt.Fprintln(w, pad+"-")
			render(w, item, indent+2)
		}
	}
}
