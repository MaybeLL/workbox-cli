package workbox

import (
	"errors"
	"os"
)

// Keep detailed initialization and worktree procedures in the global skill.
// This portable entry point contains no machine-specific installation paths.
const workspaceAgentInstructions = `# Workbox workspace

Before initializing this workspace or developing in it, load the installed global
skill named ` + "`workbox`" + ` and read its initialization and worktree reference.
Use your agent's skill loader, or read SKILL.md and its referenced guide from the
configured installation location. The default is ~/.agents/skills/workbox/;
respect a custom installation path. If the skill cannot be found, ask the user
for its location or suggest ` + "`workbox skill install`" + ` before proceeding.

The cloned repositories are analysis baselines. Keep their working files and
HEAD unchanged. Make development changes in native Git worktrees below this
workspace's .workbox/worktree/ directory, following the global workbox skill.

Start agent sessions at this workspace root. For a new session opened directly
in a repository or development worktree, explicitly load this workspace's
AGENTS.md and the global workbox skill: nested Git roots may stop automatic rule
discovery. Keep both in context and follow the repository's own instructions too.
`

func (t *ownedTree) writeAgentInstructions() *Failure {
	if e := t.check(); e != nil {
		return e
	}
	f, err := t.root.OpenFile("AGENTS.md", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fail("TARGET_EXISTS", "Workspace AGENTS.md already exists; nothing was overwritten")
		}
		return ioFailure("Could not create workspace AGENTS.md")
	}
	_, writeErr := f.WriteString(workspaceAgentInstructions)
	info, statErr := f.Stat()
	closeErr := f.Close()
	if writeErr != nil || statErr != nil || closeErr != nil {
		return ioFailure("Could not write workspace AGENTS.md")
	}
	t.owned["AGENTS.md"] = info
	return t.check()
}
