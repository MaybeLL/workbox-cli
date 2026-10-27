---
name: workbox
description: Manage reusable repository and workspace configurations with the workbox CLI, prepare Git workspaces, inspect their status, and guide initialization or development in their Git worktrees. Use when the user asks to use workbox or work with a workbox workspace.
---

# Workbox

Use the installed `workbox` executable. Check `workbox --version` and use `workbox --help` for the exact command syntax. If it is missing, install it using the user's chosen method; Homebrew users can run `brew install MaybeLL/tap/workbox`.

## CLI initialization

Configuration commands and workspace plan/create require a successful `workbox init`.
On `NOT_INITIALIZED`, establish the user's intended backend, Skill location and copy choice, then run explicit noninteractive init with `--format json`. For a requested local setup: `workbox init --backend sqlite --install-skill --format json`; use `--skip-skill` when the user does not want Skill installation. MySQL uses `--mysql-dsn-file FILE`; never put passwords in arguments or messages. Scripts require `--backend` and exactly one Skill choice. `--skill-path` is the Skills parent directory.

For a storage switch, explain source and target and obtain the copy choice; pass `--copy-config` or `--no-copy-config`. Copy retains the source, verifies Git remotes, and rejects content conflicts. Environment overrides cannot bypass initialization; conflicting overrides must be removed by the caller. Do not silently replace damaged settings, remove databases, or copy credentials into business exports.

Check `data.settings.publication`, `data.initialized`, and the per-step states. Skill failure after settings publication leaves the CLI initialized; retry `skill install` separately. A partial or unknown commit is not proof of rollback: reread/retry without deleting target data. Help, version, local status, Skill installation and uninstall remain available without init. CLI initialization configures storage; workspace initialization still follows the workflow below.

## Configuration and preparation

Use `--format json` for commands. Check the exit code, `ok`, `error`, and any partial `data`. An interrupted or failed create can leave a useful directory; inspect the result before deciding the next action.

- Discover existing names with `repo-config list` and `workspace-config list` before creating or selecting configurations. A repo configuration specifies a remote URL and an exact existing default branch. Do not guess either.
- Repo configurations support optional `--summary TEXT` on add/update; an empty string clears it. Summaries are shared navigation hints, not behavioral evidence. Workspace plans expose them. Version-1 databases are read without changes and migrate to schema 2 on the next write; do not use older binaries with migrated databases.
- A workspace configuration selects repo names and relative target directories through a JSON file, for example `[{"repo":"api","target_dir":"repos/api"}]`. It can also store an initialization script using `--init-script-file`.
- Repository selections may optionally include `"ref":{"type":"branch","value":"release/v2"}` or `"ref":{"type":"commit","value":"<full lowercase SHA>"}`. Omission inherits the Repo default; omit it in an updated repos array to clear an override. Never put a SHA in `default_branch`. Plan/create report the effective ref and resolved SHA; commit baselines have detached HEAD. Overrides do not change shared Repo defaults or existing checkouts. Commit verification may fetch into a disposable temporary repository, and inaccessible commits fail without fallback.
- Use `config export --output FILE` and `config import --input FILE` to transfer configuration. Import rejects conflicting content rather than overwriting it.
- Use `workspace plan --config NAME --path DIR` to check a proposed workspace, then `workspace create --config NAME --path DIR --format json` to prepare it. The target must not exist and its parent must exist. Plan does not reserve a target or freeze a configuration.
- Use `workspace status --path DIR --format json` to inspect real checkout state. It uses local Git state, without contacting remotes or consulting the configuration DB.

## Initialization and development

Read [the initialization and worktree workflow](references/agent-guide.md) when initializing a prepared workspace or developing within one. It defines how to verify the returned script's artifacts and keep baseline work files and HEAD unchanged.

`workspace create` returns `state: "prepared"` and the exact `init_script` from that create's snapshot. The CLI never runs the script. Preparation alone does not establish successful initialization; review the returned script within the user's authorization and verify its intended artifacts.

There are no `workbox worktree` commands in this version. Use native Git worktrees under the workspace's `.workbox/worktree/` directory according to the referenced workflow. Keep development changes and outputs in the development worktree.
