# workbox

Prepare local Workbox instances from reusable configurations. `workbox` stores repository sources and Workbox layouts in SQLite (default) or MySQL, verifies remote branches during repository registration, and prepares a directory for remote code analysis. Initialization scripts are returned to the caller as text and **never executed by the CLI**.

Workbox is written in Go and builds to a single executable with a bundled, pure-Go SQLite engine. Users need Git, with existing HTTPS/SSH authentication configured. Supported build targets are macOS and Linux on ARM64 and AMD64; Windows is not supported in this release. Development checkouts use native Git worktrees inside the same Workbox.

项目的目标场景见[产品愿景](docs/Vision.md)，已确认的职责边界见[设计决策](docs/Decisions.md)，模块和代码入口见[实现导览](docs/Implementation.md)。

## Install

Install the prebuilt executable with Homebrew:

```sh
brew install MaybeLL/tap/workbox
workbox --version
```

Or download the matching archive from [GitHub Releases](https://github.com/MaybeLL/workbox-cli/releases), verify its SHA-256 against the published checksums, then put `workbox` in PATH.

To build from this checkout, use Go 1.26+:

```sh
make build
./bin/workbox --version
```

Prebuilt release executables require no Go, Python, C compiler, or separately installed SQLite library. Add the executable's directory to PATH to use `workbox` directly.

## Initialize

Run `workbox init` after installation or upgrade. The terminal wizard selects SQLite or an existing MySQL database, offers Skill installation (default yes), and confirms before writing. Configuration commands and workbox plan/create require this step; existing databases and IDs are preserved.

For scripts: `workbox init --backend sqlite --skip-skill --format json`. Use `--install-skill` to install the companion Skill and `--skill-path DIR` to choose its parent directory.

## Install the agent skill

After installing or upgrading Workbox to 0.1.1 or later:

```sh
workbox skill install
```

This installs the single bundled `workbox` skill and its workflow reference into the shared user directory `~/.agents/skills/workbox`, supported by Codex, Cursor and OpenCode. Use `$workbox` in your next Codex message; restart Codex if it does not appear. The skill is embedded in the executable, so installation works offline and needs neither a source checkout nor Python. It does not read or create the configuration DB, run Git, or modify other skills.

To choose a different skills parent directory, use `workbox skill install --path /path/to/skills`. The installer always adds a `workbox` subdirectory. Identical content returns `state: "unchanged"`; different, incomplete, extra or symlinked content returns `SKILL_CONFLICT` without overwriting anything. To replace a different or customized version, back up and move the existing `workbox` directory aside, then rerun the command. There is no force-overwrite mode.

The global Skill reads the instance repository list and any team-supplied instructions. The global Workbox skill covers configuration, script handoff and worktree development.

Start an agent session at the Workbox root. A session started directly inside a baseline repository or a development worktree may stop discovering ancestor rules at a Git boundary or use nearer repository instructions; explicitly include the team's `AGENTS.md`, when configured, and global `workbox` skill in such sessions. Existing repository instructions remain applicable. The entry point guides agent behavior; it does not make baselines physically read-only. Existing Workbox instances are not retrofitted automatically.

## Uninstall

With Workbox 0.1.2 or later, remove the CLI and its installed companion skills together:

```sh
workbox uninstall --dry-run   # Preview without changing files
workbox uninstall
```

Homebrew installations are removed through `brew uninstall --formula --force maybell/tap/workbox`, including older installed Workbox versions. Direct archive installations remove the running `workbox` executable and its invocation symlink, if present. Settings, all saved credential revisions, configuration databases, existing Workbox instances, other skills and shared parent directories are preserved. No administrator password is requested; the installation directory must be writable by your user.

Uninstall discovers recorded skill locations and the standard `~/.agents/skills/workbox`, `~/.codex/skills/workbox` and `~/.claude/skills/workbox` directories. It verifies every skill before deleting anything. Changed, incomplete, extra or symlinked content causes `SKILL_CONFLICT`; back up and move that directory aside before retrying. A later package-manager error can leave skills already removed; JSON output reports partial results and retains configuration/Workbox instances.

Starting with 0.1.2, `skill install` records every custom installation path. For a custom path installed with 0.1.1, run `workbox skill install --path DIR` once to register identical content, or use `workbox uninstall --skill-path DIR`. Uninstall does not search the whole disk or remove archive/source checkouts. Installation records use `WORKBOX_STATE_HOME`, otherwise `${XDG_STATE_HOME:-~/.local/state}/workbox`; keep the same environment when installing and uninstalling.

## First Workbox

The URL below is a placeholder. Replace it with a repository you can access and a branch that actually exists. Git must already have credentials and, for SSH, a trusted host key.

```sh
workbox repo-config add api \
  --url https://example.org/team/api.git --default-branch develop

cat > repos.json <<'JSON'
[{"repo": "api", "target_dir": "repos/api"}]
JSON

cat > init.sh <<'SH'
#!/bin/sh
# Expected artifact: READY.txt at the Workbox root, containing ready.
printf '%s\n' 'ready' > READY.txt
SH

workbox workbox-config add analysis \
  --repos-file repos.json --init-script-file init.sh
workbox plan --config analysis --path ./analysis-one
workbox create --config analysis --path ./analysis-one --format json
workbox status --path ./analysis-one
```

The destination must be new and its parent writable. Plan checks the configuration and path locally. Create reads its own configuration snapshot and returns `state: "prepared"`, the configured repositories and the original `init_script` for separate review. Scripts that use a checkout need that repository to be requested and prepared first. See the [handoff guide](skills/workbox/references/agent-guide.md).

```text
analysis-one/
└── .workbox/
    └── repos.json
```

Start the Agent at the root. It can search and read code remotely from the listed projects, including projects in different GitLab groups. Empty configurations write an empty JSON array. Planning and creation never check out the listed business repositories. Creation retrieves explicitly selected documents and Skills. Scripts that previously consumed checkout fields such as `resolved_commit` or entered `target_dir` immediately after create must defer those steps until a checkout is requested. Only check out repositories requested by the user, at their configured `target_dir`. A request for a development worktree includes preparing its missing baseline. Place worktrees under `.workbox/worktree/`.

## Team documents and Skills

Use `workbox auth gitlab set --host gitlab.example.com --token-file FILE` after init, or pass `--gitlab-host` and `--gitlab-token-file` during init. Use a personal token with read API access. Credentials and SSH aliases stay on the user's machine.

Pass a JSON array to `workbox workbox-config add/update NAME --docs-file FILE`. An empty array clears the selection. For example:

```json
[{"project_url":"https://gitlab.example.com/team/handbook","ref":{"type":"branch","value":"main"},"source_path":"rules/review.md","target_path":"AGENTS.md"}]
```

Each create resolves a project's branch once and copies the selected files verbatim. A commit ref pins a version across creates. The root AGENTS.md exists only when explicitly configured.

```sh
workbox skill-config add --git-url https://gitlab.example.com/team/skills.git --subdir analysis
workbox skill-config add --installer npx-skills https://github.com/mattpocock/skills --skill grill-me
workbox workbox-config add-skill analysis grill-me
workbox create --config analysis --path ./demand
workbox search --path ./demand --query CreateOrder --repo api --ref api=release/11 --format json
workbox read --path ./demand --repo api --file pkg/order.go --ref release/11 --lines 12-25
```

Git sources use normal Git credentials; registration reads the name from SKILL.md. npx registration records the selected name without invoking npx. Creation installs selected Skills under `.agents/skills/`; the npx installer uses `skills@1.7.6` and requires npx with Node 22.20 or newer. Bundle companion files as regular files and directories. Remove a selection with `workbox workbox-config remove-skill NAME SKILL` before deleting a referenced registration. The global Workbox Skill remains user-installed.

Search and read are limited to repositories in the instance manifest. Use repeated `--repo` and `--ref NAME=REF` to select multiple targets. Search returns per-repository coverage and retains hits from successful pages when another page or repository fails. A pinned SHA can be read directly. Empty search results without ref evidence report unknown coverage. For an SSH alias, use `workbox auth gitlab alias set --ssh-host ssh.company --host gitlab.company:8443` and retry in the same instance.

## Per-Workbox baseline overrides

Each repository selection may include an optional `ref`. Omit it to inherit the
Repo configuration's `default_branch`, or select an existing branch or a full
lowercase commit SHA (40 hex characters for SHA-1, 64 for SHA-256):

```json
[
  {"repo": "api", "target_dir": "repos/api", "ref": {"type": "branch", "value": "release/v2"}},
  {"repo": "worker", "target_dir": "repos/worker", "ref": {"type": "commit", "value": "0123456789abcdef0123456789abcdef01234567"}}
]
```

Save this array with `workbox-config add/update NAME --repos-file FILE`.
This only changes that Workbox configuration; shared Repo defaults and existing
checkouts remain unchanged. Updating the array with `ref` omitted restores
inheritance. Explicit null, tags, short SHAs, revision expressions and unknown
fields are rejected. The same fields survive show/list, export/import and storage
backend copies; existing configurations need no migration. Older Workbox versions
do not support these overrides and must not be used to edit the same database.

Plan and create report the configured default branch and optional ref override. The Agent confirms the analysis scope and versions with the user where needed, then resolves refs when accessing the selected repositories.

## Commands

Existing scripts may still use the former `workspace-config` and `workspace plan/create/status` spellings. Stored configurations, export files and JSON response keys remain compatible.

All commands, including help and version, accept `--format json` anywhere in the command. Default output is readable text.

```text
workbox init [--backend sqlite|mysql] [--mysql-dsn-file FILE]
             [--install-skill | --skip-skill] [--skill-path DIR]
             [--copy-config | --no-copy-config]
workbox repo-config add NAME --url URL --default-branch BRANCH [--summary TEXT]
workbox repo-config list
workbox repo-config show NAME
workbox repo-config update NAME [--url URL] [--default-branch BRANCH] [--summary TEXT]
workbox repo-config remove NAME

workbox workbox-config add NAME [--repos-file FILE] [--init-script-file FILE]
workbox workbox-config list
workbox workbox-config show NAME
workbox workbox-config update NAME [--repos-file FILE] [--init-script-file FILE]
workbox workbox-config remove NAME

workbox plan --config NAME --path DIR
workbox create --config NAME --path DIR
workbox status --path DIR
workbox config export --output FILE
workbox config import --input FILE
workbox skill install [--path SKILLS_DIRECTORY]
workbox uninstall [--dry-run] [--skill-path SKILLS_DIRECTORY]
workbox --help
workbox --version
```

Names match `[a-z0-9][a-z0-9._-]{0,63}`. Updates need at least one field, preserve IDs, and validate the complete merged result. An omitted file keeps the old value; an empty script file clears the script, and `[]` clears repository selections. Input is UTF-8. A nonempty script requires a first-line shebang with an interpreter, but the interpreter is not checked or invoked. Local configuration input files are read at save time. GitLab Markdown selections store their source and target paths until creation.

Repository URLs support HTTPS, `ssh://user@host/path`, and SCP-style `git@host:path`, across hosted or self-hosted Git services. Local paths and credential-bearing URLs are rejected. Passwords/tokens must use Git's credential facilities. Store no secrets or sensitive data in configurations or scripts. Raw Git stderr is deliberately excluded from errors to avoid exposing authentication secrets.

Every add, update and nonempty import checks the exact remote `refs/heads/BRANCH`. Tags do not count and no default branch is guessed. Ambiguous inaccessible/missing remotes return `REMOTE_UNVERIFIABLE`; Git's generic access denial is not evidence of nonexistence. Queries time out after 60 seconds, and other Git operations after 60 seconds. All Git operations are noninteractive; no automatic retries occur.

Each Workbox repository entry requires `repo` and `target_dir` and accepts an optional `ref`. Repository selections cannot repeat. Target paths use `/`, must be relative, and cannot contain empty, `.`, `..`, `.git`, or `.workbox` segments. Backslashes, drive paths, and ancestor/descendant or case-folded overlaps are rejected. The top-level `AGENTS.md` name and paths below it are reserved (case-insensitive); repository targets cannot overlap the Workbox instruction file. Referenced repository configurations cannot be removed; the error lists the Workbox configurations using them. Removing a Workbox configuration never removes a directory.

## Storage and portability

SQLite is the first-run wizard default. Both backends save initialization settings under `WORKBOX_CONFIG_HOME`, otherwise `${XDG_CONFIG_HOME:-~/.config}/workbox`. SQLite uses `config.db` there; MySQL uses an existing dedicated database and saves a private immutable credential copy there. Relative roots resolve from the current directory.

For MySQL, create a private UTF-8 DSN file, for example `workbox:REPLACE_PASSWORD@tcp(db.example.org:3306)/workbox?tls=true`, then run:

```sh
workbox init --backend mysql --mysql-dsn-file /path/to/private/mysql.dsn \
  --skip-skill --no-copy-config --format json
```

Choose `--copy-config` instead when you want to copy existing configurations while retaining the source. Copy validates Git remotes and rejects conflicting target content. Workbox never creates the MySQL database itself.

`WORKBOX_DB`, `WORKBOX_MYSQL_DSN` and `WORKBOX_MYSQL_DSN_FILE` can override saved settings per command, but cannot replace initialization. MySQL still uses the local settings root. Credentials are never included in business exports or database errors. SQLite ignores MySQL credentials. For MySQL, supply at most one DSN override. A backend switch with existing configurations requires an explicit copy choice. Copy retains the source and aborts on conflicting target content.

For recovery, inspect the init result’s settings publication and copy state. If settings were published but Skill installation failed, fix the Skill conflict and retry `workbox skill install`. If a database commit or settings publication has unknown status, reread the destination before retrying. If copying succeeded before settings publication failed, keep the target data; retry reuses identical entries.

Both adapters implement the same storage interface, keeping SQL, schema initialization, transactions and locking outside business operations. MySQL needs a dedicated InnoDB database, schema-scoped SELECT, INSERT, UPDATE, DELETE, CREATE and ALTER privileges, and support for named locks. CI exercises MySQL 8.4.

Both backends store team, repository and Workbox configurations. UUID references are local to each store. Each Workbox has its own repository list on disk. Configuration edits affect future creates only.

After the settings gate, missing SQLite DBs and empty MySQL databases return empty lists/exports without initializing tables. Init itself explicitly prepares tables. A nonexistent or inaccessible MySQL database is an error. Missing configuration lookups fail without initialization. Status, help, version, skill installation and uninstall do not connect to the DB. Invalid writes and failed remote validation do not initialize a DB. Unknown DB versions are rejected without migration; damaged/unreadable DBs are errors. Short read transactions provide snapshots; network checks run outside write transactions. Writes recheck the snapshot under a transaction and reject concurrent changes with `CONFIG_CONFLICT`. Uninstall preserves both local and remote configuration.

```sh
workbox config export --output workbox-config.json --format json
workbox config import --input workbox-config.json --format json
```

Packages that contain document selections or any registered Skill export as version 2. Skill sources are verified offline on import. The version 1 package contains `schema_version`, `repo_configs`, `workspace_configs` and optional `team_configs`. It excludes UUIDs and includes full scripts. Arrays export by configuration name; Workbox repository order is preserved. A package must contain every referenced repository. Unknown fields, duplicate names/fields, invalid types, scripts or paths, and dangling references are rejected. An import verifies all packaged business repository remotes and commits the whole package in one transaction. Same-name/same-content records are no-ops (still remotely verified); different content rejects the entire package. There is no overwrite mode.

Exports use a temporary file in the output parent, fsync its contents, and publish with a no-overwrite atomic hard link. Existing files or symlinks are never replaced. The output filesystem must support hard links; otherwise the command reports `IO_ERROR` and cleans its temporary file.

## Status and failure results

`workbox status` is entirely local and read-only: no network, scripts or DB, with `GIT_OPTIONAL_LOCKS=0`. It recursively finds baseline `.git` entries in ordinary directories, stopping at each checkout (submodules/nested repositories are not separate entries). It skips directory symlinks and `.workbox`, then separately inspects **every direct child** of `.workbox/worktree/`. A worktree must be linked and verified against a discovered baseline's common Git directory and worktree records. Foreign, broken, unreadable and symlink worktree candidates are reported as errors.

Each checkout reports `kind`, relative `path`, `branch`, full `commit`, `clean`, and `error`. Detached HEAD has `branch: null`; an unborn repository has `commit: null`. Clean accounts for staged, unstaged, all untracked files and initialized submodule changes; ignored files do not count. Dirty is a successful observation. Partial failures keep readable results and scan errors, with unknown fields `null`. Status does not provide an atomic snapshot across repositories.

JSON stdout always contains exactly one envelope, even for parser errors:

```json
{"ok": true, "data": {"program": "workbox", "version": "0.2.0"}, "error": null}
```

Errors use `{"code":"...","message":"...","details":{}}`; partial results remain in `data`. Diagnostic output belongs on stderr. Create prepares files in a private sibling directory. A failed download or install reports its stage and item; fix it and retry the same destination. Exclusive publication preserves a competing destination. Cancellation after successful publication retains the prepared result. SIGINT/SIGTERM return best-effort partial JSON with exit 130; process crashes/forced kills cannot promise output.

| Exit | Meaning / codes |
| --- | --- |
| 0 | Success, dirty observations, identical imports |
| 2 | `NOT_INITIALIZED`, `INVALID_ARGUMENTS`, `CONFIG_INVALID`, `CONFIG_NOT_FOUND`, `CONFIG_CONFLICT`, `SCHEMA_UNSUPPORTED`, `PATH_INVALID`, `TARGET_EXISTS`, `SKILL_CONFLICT`, `UNINSTALL_UNSUPPORTED`, `REMOTE_BRANCH_NOT_FOUND`; `REMOTE_REPO_NOT_FOUND` is reserved for conclusive evidence |
| 1 | `COMMIT_OUTCOME_UNKNOWN`, `IO_ERROR`, `GIT_ERROR`, `REMOTE_UNVERIFIABLE`, `TIMEOUT`, `STATUS_PARTIAL`, `UNINSTALL_FAILED` |
| 130 | `INTERRUPTED` |

Directory creation is exclusive and checks directory identities and symlinks between steps. It detects common filesystem substitutions, but does not claim isolation from every hostile concurrent filesystem mutation. Baselines are a workflow convention, not physically read-only repositories. Worktrees share Git objects and some refs/metadata.

## Development

```sh
go test ./...
go test -race ./...
```

The suite uses temporary configuration directories and local Git remotes. To include MySQL integration tests, set `WORKBOX_TEST_MYSQL_DSN` to a dedicated test server before running the commands above. The test account needs permission to create and drop disposable databases and create triggers. Without that variable, the MySQL tests are skipped. CI uses an ephemeral MySQL 8.4 service.

When changing the `init` wizard, build the CLI and run it in a terminal with temporary `HOME`, `WORKBOX_CONFIG_HOME` and `WORKBOX_STATE_HOME` directories. Use the arrow keys and Enter to choose a backend, go back to edit a custom Skill path, then cancel with Esc or Ctrl+C. Check that password input stays hidden and that terminal echo and line editing recover after cancellation. Repeat with `NO_COLOR=1`, `TERM=dumb` and a narrow viewport. Check that JSON and piped invocations return without prompting.

Run `sh scripts/check-install.sh /path/to/workbox` to check an installed executable in isolation.

### Publish a release

To build the release archives locally, run `make release VERSION=0.7.0` with the intended version.

After CI passes on the release commit, push its version tag. The [release workflow](.github/workflows/release.yml) creates the draft, builds the archives and macOS bottles, and merges the bottle checksums into the final formula. If publication stops after both bottles and their metadata are uploaded, rerun the workflow manually with the draft’s `release_tag`. After publication, copy the final `workbox.rb` release asset to `Formula/workbox.rb` in the Homebrew tap. Use a new version for corrections to published artifacts.

## Repository summaries and private catalogs

Repo configurations include optional `summary` text (empty by default). Set it
with `repo-config add/update ... --summary "Service responsibilities"`; pass
`--summary ""` to clear it. Show/list, exports, backend transfers and Workbox
plans include the text. A Workbox reads the shared Repo summary rather than
storing another copy. Summaries are maintained navigation hints, not verified
code or deployment evidence. Older import packages may omit the field.

The project ignores `/local/` for private catalogs and helper scripts. Release
packaging includes only explicit public files and directories, not this directory.
Internal repository addresses, descriptions and credentials must not be committed
or bundled. A private helper can use `config import` with a repository-only
package (`workspace_configs: []`): identical entries are unchanged and conflicting
entries abort the import without overwriting local edits. No presets are installed
automatically.

## Teams and repository ownership

Teams describe ownership only; they do not grant permissions or manage members.
A team has an automatically generated local `id`, immutable unique `key`, editable
`name`, and optional `summary`. Repositories may belong to zero or more teams.

```sh
workbox team-config add platform --name "Platform" --summary "Shared services"
workbox team-config list
workbox team-config show platform
workbox team-config update platform --name "Platform Engineering"
workbox repo-config add-team api --team platform
workbox repo-config list --team platform
workbox repo-config remove-team api --team platform
workbox team-config remove platform
```

Repo add/update also accepts `--teams platform,operations` to replace the complete
membership set; `--teams ""` clears it. Omission preserves existing membership.
Add/remove-team are idempotent, require existing entities, and need no remote Git
access. Team keys follow the same lowercase name rules as repository names.
Duplicate keys are rejected. Team deletion fails while any repository references it.
Renaming a team's display name preserves its ID and all memberships.

Repo show/list expose team keys; Workbox plans resolve current team details
through the repository. Workbox instances do not store separate ownership. Export/import
uses optional `team_configs` plus repository `teams` arrays of keys, never local
team IDs. Packages must include all referenced teams. Import preserves existing
local IDs, allocates IDs for new teams, skips identical content, and atomically
rejects conflicts (including display-name or membership differences). Older
packages without teams remain supported and describe unassigned repositories.

The next write upgrades database schemas 1–3 to schema 4; reads remain read-only.
All clients sharing the database must be upgraded before writes, since older
binaries cannot read schema 4. Existing repository/Workbox IDs and references
are retained. The ignored private catalog is not automatically assigned to teams.
