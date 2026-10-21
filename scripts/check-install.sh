#!/bin/sh
# Exercise an installed executable without touching the user's configuration.
set -eu

binary=$(command -v "${1:-workbox}")
smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/workbox-install.XXXXXX")
trap 'rm -rf "$smoke_root"' EXIT HUP INT TERM

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
call repo-config list
call workspace-config add install-check
call workspace-config show install-check
call workspace plan --config install-check --path "$smoke_root/workspace"
test ! -e "$smoke_root/workspace"
call workspace create --config install-check --path "$smoke_root/workspace"
test -d "$smoke_root/workspace/.workbox/worktree"
call workspace status --path "$smoke_root/workspace"
call config export --output "$smoke_root/config.json"
WORKBOX_CONFIG_HOME="$smoke_root/imported-config" "$binary" config import \
  --input "$smoke_root/config.json" --format json
call workspace-config remove install-check
printf '%s\n' 'Installation smoke check passed.'
