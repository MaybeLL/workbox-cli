---
name: workbox
description: Manage reusable repository and Workbox configurations with the workbox CLI, prepare Workbox instances, inspect their status, and guide initialization or development in their Git worktrees. Use when the user asks to use workbox or work with a Workbox instance.
---

# Workbox

Use the installed `workbox` executable. Check `workbox --version` and use `workbox --help` for the exact command syntax. If it is missing, install it using the user's chosen method; Homebrew users can run `brew install MaybeLL/tap/workbox`.

## CLI initialization

Configuration commands and workbox plan/create require a successful `workbox init`.
On `NOT_INITIALIZED`, establish the user's intended backend, Skill location and copy choice, then run explicit noninteractive init with `--format json`. For a requested local setup: `workbox init --backend sqlite --install-skill --format json`; use `--skip-skill` when the user does not want Skill installation. MySQL uses `--mysql-dsn-file FILE`; never put passwords in arguments or messages. Scripts require `--backend` and exactly one Skill choice. `--skill-path` is the Skills parent directory.

For a storage switch, explain source and target and obtain the copy choice; pass `--copy-config` or `--no-copy-config`. Copy retains the source, verifies Git remotes, and rejects content conflicts. Environment overrides cannot bypass initialization; conflicting overrides must be removed by the caller. Do not silently replace damaged settings, remove databases, or copy credentials into business exports.

Check `data.settings.publication`, `data.initialized`, and the per-step states. Skill failure after settings publication leaves the CLI initialized; retry `skill install` separately. A partial or unknown commit is not proof of rollback: reread/retry without deleting target data. Help, version, local status, Skill installation and uninstall remain available without init. CLI initialization configures storage; Workbox initialization still follows the workflow below.

## Configuration and preparation

Use `--format json` for commands. Check the exit code, `ok`, `error`, and any partial `data`. If preparation fails, inspect the item and stage, fix the cause and retry with the same destination. Create publishes the whole instance once all items are ready.

- Discover existing names with `repo-config list` and `workbox-config list` before creating or selecting configurations. A repo configuration specifies a remote URL and an exact existing default branch. Do not guess either.
- Repo configurations support optional `--summary TEXT` on add/update; an empty string clears it. Summaries are shared navigation hints, not behavioral evidence. Workbox plans expose them. Version-1 databases are read without changes and migrate to schema 4 on the next write; do not use older binaries with migrated databases.
- A Workbox configuration selects repo names and relative target directories through a JSON file, for example `[{"repo":"api","target_dir":"repos/api"}]`. It can also store an initialization script using `--init-script-file`.
- Repository selections may optionally include `"ref":{"type":"branch","value":"release/v2"}` or `"ref":{"type":"commit","value":"<full lowercase SHA>"}`. Omission inherits the Repo default; omit it in an updated repos array to clear an override. Never put a SHA in `default_branch`. Plan/create list configured refs as hints for analysis. Resolve the version when accessing a selected repository.
- Use `config export --output FILE` and `config import --input FILE` to transfer configuration. Import rejects conflicting content rather than overwriting it.
- Use `workbox plan --config NAME --path DIR` to check a proposed Workbox, then `workbox create --config NAME --path DIR --format json` to prepare it. The target must not exist and its parent must exist. Plan checks the configuration and destination locally. Create writes .workbox/repos.json and any explicitly configured GitLab documents. Start analysis with the team instructions if present and the repository manifest. Only obtain local sources for repositories requested by the user, at their configured target_dir.
- Use `workbox status --path DIR --format json` to inspect real checkout state. It uses local Git state, without contacting remotes or consulting the configuration DB.

## Initialization and development

Read [the initialization and worktree workflow](references/agent-guide.md) when initializing a prepared Workbox or developing within one. It defines how to verify the returned script's artifacts and keep baseline work files and HEAD unchanged.

`workbox create` returns `state: "prepared"` and the exact `init_script` from that create's snapshot. The CLI never runs the script. Preparation alone does not establish successful initialization; review the returned script within the user's authorization and verify its intended artifacts.

There are no `workbox worktree` commands in this version. Use native Git worktrees under the Workbox's `.workbox/worktree/` directory according to the referenced workflow. Keep development changes and outputs in the development worktree.

## Teams

Use `team-config add KEY --name NAME [--summary TEXT]`, list/show/update/remove
for ownership metadata. Keys are immutable; names are display labels. Repos may
have multiple teams: use `repo-config add-team/remove-team NAME --team KEY` or
replace the set with add/update `--teams KEY1,KEY2` (empty clears). List repos with
`--team KEY`. Referenced teams cannot be removed. No member/permission management.
Export/import carries team keys and self-contained team configs, not local IDs.
Database writes upgrade schemas 1–3 to 4; upgrade shared clients first. Never infer
or assign organizational ownership without user-provided evidence.

## Remote analysis

Read .workbox/repos.json for the repositories in this Workbox instance. Each entry gives
its source_url, summary, configured default_branch, optional ref override, and
target_dir for a future checkout. These are configuration hints. Use the user's
requested repositories and versions; confirm any unclear scope or refs together
before the first cross-repository search. Later instructions override earlier ones.

Use `workbox search --path DIR --query TERM --format json`. Limit the search using repeated `--repo NAME` and `--ref NAME=REF`. Check each repository's coverage and error, including partial results on nonzero exit. Use `workbox read --path DIR --repo NAME --ref REF --file PATH --lines START-END --format json` to verify source and cite the returned commit. If the evidence is insufficient, explain the gap and ask before checking out code. Supply an SSH alias with `workbox auth gitlab alias set --ssh-host SSH_HOST --host HTTPS_HOST` when GitLab uses separate domains.

## Register and select team content

- If asked to register `npx skills add URL --skill NAME`, translate it to `workbox skill-config add --installer npx-skills URL --skill NAME`. Register only. When asked to select it for a Workbox, run `workbox workbox-config add-skill CONFIG NAME`. Creation performs installation.
- For Git, use `workbox skill-config add --git-url URL [--subdir DIR] [--branch BRANCH | --commit SHA]` and use the name returned from SKILL.md.
- Use `--docs-file FILE` on Workbox configuration add/update for explicit GitLab Markdown sources. Each entry contains project_url, ref, source_path and target_path; include AGENTS.md only when the team requests it. Personal GitLab tokens are configured by host via init or `auth gitlab set --host HOST --token-file FILE`.
