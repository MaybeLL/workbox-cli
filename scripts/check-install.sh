#!/bin/sh
# Exercise an installed executable without touching the user's configuration.
set -eu

binary=$(command -v "${1:-workbox}")
smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/workbox-install.XXXXXX")
trap 'rm -rf "$smoke_root"' EXIT HUP INT TERM
mkdir -p "$smoke_root/home"
export HOME="$smoke_root/home"
export WORKBOX_STATE_HOME="$smoke_root/state"
export WORKBOX_DB=sqlite
unset WORKBOX_MYSQL_DSN WORKBOX_MYSQL_DSN_FILE WORKBOX_TEST_MYSQL_DSN

call() {
  WORKBOX_CONFIG_HOME="$smoke_root/config" "$binary" "$@" --format json
}

git --version
call --version
call --help
call skill install --path "$smoke_root/skills"
test -f "$smoke_root/skills/workbox/SKILL.md"
test -f "$smoke_root/skills/workbox/references/agent-guide.md"
call skill install --path "$smoke_root/skills"
test ! -e "$smoke_root/config"
call init --backend sqlite --skip-skill
call repo-config list
call workspace-config add install-check
call workspace-config show install-check
call workspace plan --config install-check --path "$smoke_root/workspace"
test ! -e "$smoke_root/workspace"
call workspace create --config install-check --path "$smoke_root/workspace"
test -d "$smoke_root/workspace/.workbox/worktree"
test -s "$smoke_root/workspace/AGENTS.md"
test ! -e "$smoke_root/workspace/.agents"
cp "$smoke_root/workspace/AGENTS.md" "$smoke_root/agents-before.md"
call workspace status --path "$smoke_root/workspace"
call config export --output "$smoke_root/config.json"
WORKBOX_CONFIG_HOME="$smoke_root/imported-config" "$binary" init --backend sqlite --skip-skill --format json
WORKBOX_CONFIG_HOME="$smoke_root/imported-config" "$binary" config import \
  --input "$smoke_root/config.json" --format json
call workspace-config remove install-check
# Exercise real self-unlink only on a disposable copy, never the supplied binary.
mkdir -p "$smoke_root/bin"
cp "$binary" "$smoke_root/bin/workbox"
cp "$smoke_root/config/config.db" "$smoke_root/config-before.db"
cp "$smoke_root/config/settings.json" "$smoke_root/settings-before.json"
WORKBOX_CONFIG_HOME="$smoke_root/config" "$smoke_root/bin/workbox" skill install --format json
WORKBOX_CONFIG_HOME="$smoke_root/config" "$smoke_root/bin/workbox" uninstall --dry-run --format json
test -f "$smoke_root/bin/workbox"
WORKBOX_CONFIG_HOME="$smoke_root/config" "$smoke_root/bin/workbox" uninstall --format json
test ! -e "$smoke_root/bin/workbox"
test ! -e "$smoke_root/skills/workbox"
test ! -e "$HOME/.agents/skills/workbox"
test ! -e "$WORKBOX_STATE_HOME"
test -d "$smoke_root/workspace/.workbox/worktree"
cmp "$smoke_root/agents-before.md" "$smoke_root/workspace/AGENTS.md"
cmp "$smoke_root/config-before.db" "$smoke_root/config/config.db"
cmp "$smoke_root/settings-before.json" "$smoke_root/config/settings.json"
test -x "$binary"
printf '%s\n' 'Installation smoke check passed.'
