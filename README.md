# workbox

Prepare local Git workspaces from reusable configurations. `workbox` stores repository sources and workspace layouts in SQLite (default) or MySQL, validates real remote branches, and clones clean analysis baselines. Initialization scripts are returned to the caller as text and **never executed by the CLI**.

Workbox is written in Go and builds to a single executable with a bundled, pure-Go SQLite engine. Users need Git, with existing HTTPS/SSH authentication configured. Supported build targets are macOS and Linux on ARM64 and AMD64; Windows is not supported in this release. Development checkouts use native Git worktrees inside the same workspace.

## Install

Install the prebuilt executable with Homebrew:

```sh
brew install MaybeLL/tap/workbox
workbox --version
```

Or download the matching archive from [GitHub Releases](https://github.com/MaybeLL/workbox-cli/releases), verify its checksum and put `workbox` in PATH. See [installation and release instructions](docs/distribution.md).

To build from this checkout, use Go 1.26+:

```sh
make build
./bin/workbox --version
```

Prebuilt release executables require no Go, Python, C compiler, or separately installed SQLite library. Add the executable's directory to PATH to use `workbox` directly. Configuration schema version 1 remains compatible across implementations; no data migration is needed.

## Initialize

Run `workbox init` after installation or upgrade. The terminal wizard selects SQLite or an existing MySQL database, offers Skill installation (default yes), and confirms before writing. Configuration commands and workspace plan/create require this step; existing databases and IDs are preserved.

For scripts: `workbox init --backend sqlite --skip-skill --format json`. Use `--install-skill` to install the companion Skill and `--skill-path DIR` to choose its parent directory. See [initialization, switching and recovery](docs/initialization.md).

## Install the agent skill

After installing or upgrading Workbox to 0.1.1 or later:

```sh
workbox skill install
```

This installs the single bundled `workbox` skill and its workflow reference into the shared user directory `~/.agents/skills/workbox`, supported by Codex, Cursor and OpenCode. Use `$workbox` in your next Codex message; restart Codex if it does not appear. The skill is embedded in the executable, so installation works offline and needs neither a source checkout nor Python. It does not read or create the configuration DB, run Git, or modify other skills.

To choose a different skills parent directory, use `workbox skill install --path /path/to/skills`. The installer always adds a `workbox` subdirectory. Identical content returns `state: "unchanged"`; different, incomplete, extra or symlinked content returns `SKILL_CONFLICT` without overwriting anything. To replace a different or customized version, back up and move the existing `workbox` directory aside, then rerun the command. There is no force-overwrite mode.

Workbox uses one global skill for configuration, initialization and development. Starting with 0.1.3, `workspace create` also writes a short `AGENTS.md` at the new workspace root. This entry point tells agents to load the global `workbox` skill and its workflow reference; detailed rules stay in that skill. No project-local `workbox-dev` skill is installed. If a global skill was installed with `--path`, use that configured location. If it cannot be found, the entry point asks the agent to confirm its location or suggest installation before proceeding.

Start an agent session at the workspace root. A session started directly inside a baseline repository or a development worktree may stop discovering ancestor rules at a Git boundary or use nearer repository instructions; explicitly include the workspace's `AGENTS.md` and global `workbox` skill in such sessions. Existing repository instructions remain applicable. The entry point guides agent behavior; it does not make baselines physically read-only. Existing workspaces are not retrofitted automatically.

## Uninstall

With Workbox 0.1.2 or later, remove the CLI and its installed companion skills together:

```sh
workbox uninstall --dry-run   # Preview without changing files
workbox uninstall
```

Homebrew installations are removed through `brew uninstall --formula --force maybell/tap/workbox`, including older installed Workbox versions. Direct archive installations remove the running `workbox` executable and its invocation symlink, if present. Settings, all saved credential revisions, configuration databases, existing workspaces, other skills and shared parent directories are preserved. No administrator password is requested; the installation directory must be writable by your user.

Uninstall discovers recorded skill locations and the standard `~/.agents/skills/workbox`, `~/.codex/skills/workbox` and `~/.claude/skills/workbox` directories. It verifies every skill before deleting anything. Changed, incomplete, extra or symlinked content causes `SKILL_CONFLICT`; back up and move that directory aside before retrying. A later package-manager error can leave skills already removed; JSON output reports partial results and retains configuration/workspaces.

Starting with 0.1.2, `skill install` records every custom installation path. For a custom path installed with 0.1.1, run `workbox skill install --path DIR` once to register identical content, or use `workbox uninstall --skill-path DIR`. Uninstall does not search the whole disk or remove archive/source checkouts. Installation records use `WORKBOX_STATE_HOME`, otherwise `${XDG_STATE_HOME:-~/.local/state}/workbox`; keep the same environment when installing and uninstalling.

## First workspace

The URL below is a placeholder. Replace it with a repository you can access and a branch that actually exists. Git must already have credentials and, for SSH, a trusted host key.

```sh
workbox repo-config add api \
  --url https://example.org/team/api.git --default-branch develop

cat > repos.json <<'JSON'
[{"repo": "api", "target_dir": "repos/api"}]
JSON

cat > init.sh <<'SH'
#!/bin/sh
# Expected artifact: READY.txt at the workspace root, containing ready.
printf '%s\n' 'ready' > READY.txt
SH

workbox workspace-config add analysis \
  --repos-file repos.json --init-script-file init.sh
workbox workspace plan --config analysis --path ./analysis-one
workbox workspace create --config analysis --path ./analysis-one --format json
workbox workspace status --path ./analysis-one
```

The target must not exist, including an existing empty directory. Its parent must already exist and be writable. Creation returns `state: "prepared"`, each actual branch and full commit, and the complete `init_script` from the **same configuration snapshot**. It does not create `READY.txt`. An Agent or human reviews and runs that returned text separately, then verifies the promised artifact. See [the Agent handoff and worktree guide](docs/agent-guide.md).

```text
analysis-one/
├── AGENTS.md                  points agents to the global workbox skill
├── repos/api/                 analysis baseline; keep its work files and HEAD unchanged
└── .workbox/
    └── worktree/              development worktrees are direct children
```

Clone is full-history, single-branch, without recursive submodules. `plan` checks paths and remotes without creating anything. `create` independently rechecks using a new snapshot; a previous plan is not a lock. Empty workspace configurations are allowed and still create `.workbox/worktree/`.

## Commands

All commands, including help and version, accept `--format json` anywhere in the command. Default output is readable text.

```text
workbox init [--backend sqlite|mysql] [--mysql-dsn-file FILE]
             [--install-skill | --skip-skill] [--skill-path DIR]
             [--copy-config | --no-copy-config]
workbox repo-config add NAME --url URL --default-branch BRANCH
workbox repo-config list
workbox repo-config show NAME
workbox repo-config update NAME [--url URL] [--default-branch BRANCH]
workbox repo-config remove NAME

workbox workspace-config add NAME [--repos-file FILE] [--init-script-file FILE]
workbox workspace-config list
workbox workspace-config show NAME
workbox workspace-config update NAME [--repos-file FILE] [--init-script-file FILE]
workbox workspace-config remove NAME

workbox workspace plan --config NAME --path DIR
workbox workspace create --config NAME --path DIR
workbox workspace status --path DIR
workbox config export --output FILE
workbox config import --input FILE
workbox skill install [--path SKILLS_DIRECTORY]
workbox uninstall [--dry-run] [--skill-path SKILLS_DIRECTORY]
workbox --help
workbox --version
```

Names match `[a-z0-9][a-z0-9._-]{0,63}`. Updates need at least one field, preserve IDs, and validate the complete merged result. An omitted file keeps the old value; an empty script file clears the script, and `[]` clears repository selections. Input is UTF-8. A nonempty script requires a first-line shebang with an interpreter, but the interpreter is not checked or invoked. Files are read at save time; their paths are not stored.

Repository URLs support HTTPS, `ssh://user@host/path`, and SCP-style `git@host:path`, across hosted or self-hosted Git services. Local paths and credential-bearing URLs are rejected. Passwords/tokens must use Git's credential facilities. Store no secrets or sensitive data in configurations or scripts. Raw Git stderr is deliberately excluded from errors to avoid exposing authentication secrets.

Every add, update and nonempty import checks the exact remote `refs/heads/BRANCH`. Tags do not count and no default branch is guessed. Ambiguous inaccessible/missing remotes return `REMOTE_UNVERIFIABLE`; Git's generic access denial is not evidence of nonexistence. Queries time out after 60 seconds, clones after 1800 seconds per repository, and other Git operations after 60 seconds. All Git operations are noninteractive; no automatic retries occur.

Workspace repository lists contain only `repo` and `target_dir`. Repository selections cannot repeat. Target paths use `/`, must be relative, and cannot contain empty, `.`, `..`, `.git`, or `.workbox` segments. Backslashes, drive paths, and ancestor/descendant or case-folded overlaps are rejected. The top-level `AGENTS.md` name and paths below it are reserved (case-insensitive); repository targets cannot overlap the workspace instruction file. Referenced repository configurations cannot be removed; the error lists the workspace configurations using them. Removing a workspace configuration never removes a directory.

## Storage and portability

SQLite is the first-run wizard default. Both backends save initialization settings under `WORKBOX_CONFIG_HOME`, otherwise `${XDG_CONFIG_HOME:-~/.config}/workbox`. SQLite uses `config.db` there; MySQL uses an existing dedicated database and saves a private immutable credential copy there. Relative roots resolve from the current directory. Schema version 1 remains compatible with previous releases.

For MySQL, create a private UTF-8 DSN file, for example `workbox:REPLACE_PASSWORD@tcp(db.example.org:3306)/workbox?tls=true`, then run:

```sh
workbox init --backend mysql --mysql-dsn-file /path/to/private/mysql.dsn \
  --skip-skill --no-copy-config --format json
```

Choose `--copy-config` instead when you want to copy existing configurations while retaining the source. Copy validates Git remotes and rejects conflicting target content. Workbox never creates the MySQL database itself.

`WORKBOX_DB`, `WORKBOX_MYSQL_DSN` and `WORKBOX_MYSQL_DSN_FILE` can override saved settings per command, but cannot replace initialization. MySQL still uses the local settings root. Credentials are never included in business exports or database errors. See [initialization](docs/initialization.md) for exact precedence, noninteractive arguments and partial-failure recovery.

Both adapters implement the same storage interface, keeping SQL, schema initialization, transactions and locking outside business operations. See [backend setup, migration and adapter contracts](docs/storage.md).

Both backends have two business tables, `repo_config` and `workspace_config`; MySQL also has a `workbox_schema` metadata table. UUID references are local to each store. There is no workspace instance registry or manifest. Configuration edits affect future creates only.

After the settings gate, missing SQLite DBs and empty MySQL databases return empty lists/exports without initializing tables. Init itself explicitly prepares tables. A nonexistent or inaccessible MySQL database is an error. Missing configuration lookups fail without initialization. Status, help, version, skill installation and uninstall do not connect to the DB. Invalid writes and failed remote validation do not initialize a DB. Unknown DB versions are rejected without migration; damaged/unreadable DBs are errors. Short read transactions provide snapshots; network checks run outside write transactions. Writes recheck the snapshot under a transaction and reject concurrent changes with `CONFIG_CONFLICT`. Uninstall preserves both local and remote configuration.

```sh
workbox config export --output workbox-config.json --format json
workbox config import --input workbox-config.json --format json
```

The version 1 package contains exactly `schema_version`, `repo_configs`, and `workspace_configs`. It excludes UUIDs and includes full scripts. Arrays export by configuration name; workspace repository order is preserved. A package must contain every referenced repository. Unknown fields, duplicate names/fields, invalid types, scripts or paths, and dangling references are rejected. An import verifies all packaged remotes and commits the whole package in one transaction. Same-name/same-content records are no-ops (still remotely verified); different content rejects the entire package. There is no overwrite mode.

Exports use a temporary file in the output parent, fsync its contents, and publish with a no-overwrite atomic hard link. Existing files or symlinks are never replaced. The output filesystem must support hard links; otherwise the command reports `IO_ERROR` and cleans its temporary file.

## Status and failure results

`workspace status` is entirely local and read-only: no network, scripts or DB, with `GIT_OPTIONAL_LOCKS=0`. It recursively finds baseline `.git` entries in ordinary directories, stopping at each checkout (submodules/nested repositories are not separate entries). It skips directory symlinks and `.workbox`, then separately inspects **every direct child** of `.workbox/worktree/`. A worktree must be linked and verified against a discovered baseline's common Git directory and worktree records. Foreign, broken, unreadable and symlink worktree candidates are reported as errors.

Each checkout reports `kind`, relative `path`, `branch`, full `commit`, `clean`, and `error`. Detached HEAD has `branch: null`; an unborn repository has `commit: null`. Clean accounts for staged, unstaged, all untracked files and initialized submodule changes; ignored files do not count. Dirty is a successful observation. Partial failures keep readable results and scan errors, with unknown fields `null`. Status does not provide an atomic snapshot across repositories.

JSON stdout always contains exactly one envelope, even for parser errors:

```json
{"ok": true, "data": {"program": "workbox", "version": "0.2.0"}, "error": null}
```

Errors use `{"code":"...","message":"...","details":{}}`; partial results remain in `data`. Diagnostic output belongs on stderr. Create errors retain the directory and report each repository as `completed`, `failed` or `not_attempted`, including actual commits already read. There is no rollback, overwrite, resume, or cleanup. Use a fresh target or explicitly handle the existing directory. SIGINT/SIGTERM return best-effort partial JSON with exit 130; process crashes/forced kills cannot promise output.

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

Tests isolate all configuration state and Git configuration in temporary directories. HTTPS-shaped URLs are redirected by Git's `insteadOf` to temporary local remotes, so actual branch queries, clones, status, submodules, and linked worktrees run without network or user credentials. Fault injection covers concurrent writes, path races, cancellation, timeout and partial clone failures. Actual permission failures and process signals are also checked. MySQL integration tests run only with an explicit `WORKBOX_TEST_MYSQL_DSN` and create random, test-owned databases; CI uses an ephemeral MySQL 8.4 service. No tests use deployment credentials or working repositories. See [test contracts](docs/testing.md).

`make release VERSION=0.2.0` cross-compiles the four targets, creates archives and SHA-256 checksums, and generates a source formula pinned to the release's Git revision. The release workflow builds native macOS Homebrew bottles and adds their checksums before publishing. The output directory must not already exist; build failures preserve partial artifacts. Run `sh scripts/check-install.sh workbox` to check an installed executable with temporary configuration and workspace directories. See [distribution and release instructions](docs/distribution.md).

Version 0.2 intentionally excludes script execution, worktree create/delete commands, baseline synchronization, branch overrides, automatic schema migrations, import overwrite, background jobs and persistent workspace state.
