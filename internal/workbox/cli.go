package workbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type app struct {
	store     configStore
	gitRunner gitRunner
}
type command struct {
	entity, action, name string
	flags                map[string]string
}
type envelope struct {
	OK    bool     `json:"ok"`
	Data  any      `json:"data"`
	Error *Failure `json:"error"`
}

const usage = `Usage: workbox COMMAND [--format text|json]

  repo-config add NAME --url URL --default-branch BRANCH
  repo-config list
  repo-config show NAME
  repo-config update NAME [--url URL] [--default-branch BRANCH]
  repo-config remove NAME
  workspace-config add NAME [--repos-file FILE] [--init-script-file FILE]
  workspace-config list
  workspace-config show NAME
  workspace-config update NAME [--repos-file FILE] [--init-script-file FILE]
  workspace-config remove NAME
  config export --output FILE
  config import --input FILE
  workspace plan --config NAME --path DIR
  workspace create --config NAME --path DIR
  workspace status --path DIR
  skill install [--path SKILLS_DIRECTORY]
  uninstall [--dry-run] [--skill-path SKILLS_DIRECTORY]
  --help
  --version

Every command supports --format json; text is the default.
Scripts are stored and returned to the caller, never executed.
Workspace create writes a root AGENTS.md pointing agents to the global workbox skill.
Skill install copies the bundled skill to ~/.agents/skills/workbox by default.
--path selects a different skills parent directory. Existing differences are never overwritten.
Uninstall removes the CLI and unmodified companion skills; configuration and workspaces are preserved.
--dry-run previews uninstall. --skill-path locates a custom skill installed by an older version.

Storage: WORKBOX_DB=sqlite (default) or mysql.
SQLite uses WORKBOX_CONFIG_HOME, otherwise ${XDG_CONFIG_HOME:-~/.config}/workbox.
MySQL requires WORKBOX_MYSQL_DSN or WORKBOX_MYSQL_DSN_FILE, selecting a dedicated existing database.
Changing the backend does not copy data; use config export/import to transfer configurations.
`

func outputFormat(args []string) ([]string, string, *Failure) {
	clean := []string{}
	format := "text"
	// Preserve JSON error output even when another argument is invalid.
	for i, arg := range args {
		if arg == "--format=json" || (arg == "--format" && i+1 < len(args) && args[i+1] == "json") {
			format = "json"
		}
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--format" {
			i++
			if i == len(args) {
				return clean, format, fail("INVALID_ARGUMENTS", "--format requires text or json")
			}
			format = args[i]
		} else if strings.HasPrefix(arg, "--format=") {
			format = strings.TrimPrefix(arg, "--format=")
		} else {
			clean = append(clean, arg)
		}
	}
	if format != "text" && format != "json" {
		return clean, format, fail("INVALID_ARGUMENTS", "--format requires text or json")
	}
	return clean, format, nil
}
func parse(args []string) (command, *Failure) {
	c := command{flags: map[string]string{}}
	bad := fail("INVALID_ARGUMENTS", "Invalid command arguments; use --help for syntax")
	if len(args) == 0 {
		return c, bad
	}
	c.entity = args[0]
	start := 1
	if c.entity != "uninstall" {
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
	case "uninstall":
		allowed["dry-run"] = true
		allowed["skill-path"] = true
	case "repo-config", "workspace-config":
		switch c.action {
		case "add", "update":
			if c.entity == "repo-config" {
				allowed["url"] = true
				allowed["default-branch"] = true
				if c.action == "add" {
					required = []string{"url", "default-branch"}
				}
			} else {
				allowed["repos-file"] = true
				allowed["init-script-file"] = true
			}
			needsName = true
		case "show", "remove":
			needsName = true
		case "list":
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
	case "workspace":
		switch c.action {
		case "plan", "create":
			required = []string{"config", "path"}
			allowed["config"] = true
			allowed["path"] = true
		case "status":
			required = []string{"path"}
			allowed["path"] = true
		default:
			return c, bad
		}
	case "skill":
		if c.action != "install" {
			return c, bad
		}
		allowed["path"] = true
	default:
		return c, bad
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
			c.flags[flag] = val
		} else {
			positional = append(positional, arg)
		}
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
	return c, nil
}
func (a *app) execute(ctx context.Context, c command) (any, *Failure) {
	if ctx.Err() != nil {
		return nil, fail("INTERRUPTED", "Operation interrupted")
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
	if c.entity == "workspace" && c.action == "status" {
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
	case "repo-config", "workspace-config":
		return a.crud(ctx, c)
	case "config":
		if c.action == "export" {
			return a.export(ctx, c.flags["output"])
		}
		return a.importFile(ctx, c.flags["input"])
	default:
		return a.prepare(ctx, c.flags["config"], c.flags["path"], c.action == "create")
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
			data = map[string]any{"usage": usage, "commands": []string{"repo-config add/list/show/update/remove", "workspace-config add/list/show/update/remove", "config export/import", "workspace plan/create/status", "skill install", "uninstall"}}
		} else if ver && len(clean) == 1 {
			data = map[string]string{"program": "workbox", "version": version}
		} else {
			var c command
			c, e = parse(clean)
			if e == nil {
				data, e = a.execute(ctx, c)
			}
		}
	}
	if ctx.Err() != nil && (e == nil || e.Code != "INTERRUPTED") {
		interrupted := fail("INTERRUPTED", "Operation interrupted")
		interrupted.Data = data
		if e != nil {
			interrupted.Data = e.Data
		}
		if partial, ok := interrupted.Data.(map[string]any); ok && partial["state"] != nil {
			partial["state"] = "failed"
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
