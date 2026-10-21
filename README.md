# workbox

Prepare local Git workspaces from reusable configurations. `workbox` stores repository sources and workspace layouts in SQLite, validates real remote branches, and clones clean analysis baselines. Initialization scripts are returned to the caller as text and **never executed by the CLI**.

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
├── repos/api/                 analysis baseline; keep its work files and HEAD unchanged
└── .workbox/
    └── worktree/              development worktrees are direct children
```

Clone is full-history, single-branch, without recursive submodules. `plan` checks paths and remotes without creating anything. `create` independently rechecks using a new snapshot; a previous plan is not a lock. Empty workspace configurations are allowed and still create `.workbox/worktree/`.

## Commands

All commands, including help and version, accept `--format json` anywhere in the command. Default output is readable text.

```text
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
workbox --help
workbox --version
```

Names match `[a-z0-9][a-z0-9._-]{0,63}`. Updates need at least one field, preserve IDs, and validate the complete merged result. An omitted file keeps the old value; an empty script file clears the script, and `[]` clears repository selections. Input is UTF-8. A nonempty script requires a first-line shebang with an interpreter, but the interpreter is not checked or invoked. Files are read at save time; their paths are not stored.

Repository URLs support HTTPS, `ssh://user@host/path`, and SCP-style `git@host:path`, across hosted or self-hosted Git services. Local paths and credential-bearing URLs are rejected. Passwords/tokens must use Git's credential facilities. Store no secrets or sensitive data in configurations or scripts. Raw Git stderr is deliberately excluded from errors to avoid exposing authentication secrets.

Every add, update and nonempty import checks the exact remote `refs/heads/BRANCH`. Tags do not count and no default branch is guessed. Ambiguous inaccessible/missing remotes return `REMOTE_UNVERIFIABLE`; Git's generic access denial is not evidence of nonexistence. Queries time out after 60 seconds, clones after 1800 seconds per repository, and other Git operations after 60 seconds. All Git operations are noninteractive; no automatic retries occur.

Workspace repository lists contain only `repo` and `target_dir`. Repository selections cannot repeat. Target paths use `/`, must be relative, and cannot contain empty, `.`, `..`, `.git`, or `.workbox` segments. Backslashes, drive paths, and ancestor/descendant or case-folded overlaps are rejected. Referenced repository configurations cannot be removed; the error lists the workspace configurations using them. Removing a workspace configuration never removes a directory.

## Storage and portability

The DB is `config.db` below the first applicable configuration root:

1. Nonempty `WORKBOX_CONFIG_HOME`.
2. `${XDG_CONFIG_HOME:-~/.config}/workbox`.

Relative roots resolve from the current directory. Only two business tables exist: `repo_config` and `workspace_config`; schema version is `PRAGMA user_version=1`. UUID references are local. There is no workspace instance registry or manifest. Configuration edits affect future creates only.

Missing-DB lists and exports return empty configurations without creating a DB. Missing configuration lookups fail without creating one. Status, help, and version do not read the DB. Invalid writes and failed remote validation do not initialize a DB. Unknown DB versions are rejected without migration; damaged/unreadable DBs are errors. Short read transactions provide snapshots; network checks run outside write transactions. Writes recheck the snapshot under a transaction and reject concurrent changes with `CONFIG_CONFLICT`.

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
{"ok": true, "data": {"program": "workbox", "version": "0.1.0"}, "error": null}
```

Errors use `{"code":"...","message":"...","details":{}}`; partial results remain in `data`. Diagnostic output belongs on stderr. Create errors retain the directory and report each repository as `completed`, `failed` or `not_attempted`, including actual commits already read. There is no rollback, overwrite, resume, or cleanup. Use a fresh target or explicitly handle the existing directory. SIGINT/SIGTERM return best-effort partial JSON with exit 130; process crashes/forced kills cannot promise output.

| Exit | Meaning / codes |
| --- | --- |
| 0 | Success, dirty observations, identical imports |
| 2 | `INVALID_ARGUMENTS`, `CONFIG_INVALID`, `CONFIG_NOT_FOUND`, `CONFIG_CONFLICT`, `SCHEMA_UNSUPPORTED`, `PATH_INVALID`, `TARGET_EXISTS`, `REMOTE_BRANCH_NOT_FOUND`; `REMOTE_REPO_NOT_FOUND` is reserved for conclusive evidence |
| 1 | `IO_ERROR`, `GIT_ERROR`, `REMOTE_UNVERIFIABLE`, `TIMEOUT`, `STATUS_PARTIAL` |
| 130 | `INTERRUPTED` |

Directory creation is exclusive and checks directory identities and symlinks between steps. It detects common filesystem substitutions, but does not claim isolation from every hostile concurrent filesystem mutation. Baselines are a workflow convention, not physically read-only repositories. Worktrees share Git objects and some refs/metadata.

## Development

```sh
go test ./...
go test -race ./...
```

Tests isolate all configuration state and Git configuration in temporary directories. HTTPS-shaped URLs are redirected by Git's `insteadOf` to temporary local remotes, so actual branch queries, clones, status, submodules, and linked worktrees run without network or user credentials. Fault injection covers concurrent writes, path races, cancellation, timeout and partial clone failures. Actual permission failures and process signals are also checked. No tests touch the user's DB or working repositories. See [test contracts](docs/testing.md).

`make release VERSION=0.1.0` cross-compiles the four targets, creates archives and SHA-256 checksums, and generates a source formula pinned to the release's Git revision. The release workflow builds native macOS Homebrew bottles and adds their checksums before publishing. The output directory must not already exist; build failures preserve partial artifacts. Run `sh scripts/check-install.sh workbox` to check an installed executable with temporary configuration and workspace directories. See [distribution and release instructions](docs/distribution.md).

Version 0.1 intentionally excludes script execution, worktree create/delete commands, baseline synchronization, branch overrides, migrations, import overwrite, background jobs and persistent workspace state.
