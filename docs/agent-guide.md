# Agent handoff and development guide

The maintained, self-contained workflow is part of the bundled [workbox skill](../skills/workbox/SKILL.md). Read its [initialization and Git worktree reference](../skills/workbox/references/agent-guide.md) when initializing a prepared workspace or developing within one.

Install it for Codex with `workbox skill install` (Workbox 0.1.1+). The command installs both files from the executable; it needs no network or source checkout. Use `$workbox` in your next Codex message.

Workbox 0.1.3+ creates a short root `AGENTS.md` in each new workspace to direct agents to this global skill. Keep the detailed workflow in the skill and its reference; no `workbox-dev` skill is needed. Start sessions at the workspace root, or explicitly include its `AGENTS.md` and the global skill when starting inside a nested repository or development worktree. This entry point is an instruction for the agent, not an automatic skill loader or a filesystem permission boundary. Baseline repositories and existing workspaces are not rewritten.

## CLI setup

Storage-dependent commands require `workbox init` first. See [initialization](initialization.md). This configures the CLI connection and optional Skill; it does not initialize a prepared workspace or execute scripts.
