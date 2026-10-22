# Agent handoff and development guide

`workbox` prepares directories and returns script text. The Agent is responsible for initialization and subsequent development. Commands support JSON envelopes; check `ok`, the process exit code and partial results rather than relying on printed success text.

Before storage-dependent commands, complete CLI initialization using the `SKILL.md` instructions. A successful `workbox init` configures storage; it does not execute workspace initialization scripts.

## Initialize a prepared workspace

1. Call `workbox workspace create --config NAME --path DIR --format json`. Continue only if it returns `ok: true` and `data.state: "prepared"`. Retain this response's `workspace_path`, repository paths/commits, and `init_script`. Do not call `show` to choose a possibly newer script for this create.
2. Inspect every baseline's HEAD and `git status --porcelain=v1 --untracked-files=all --ignore-submodules=none`. Record the results. The baseline's working files and HEAD must not be changed by initialization or development.
3. For empty script text, there are no additional script tasks. For nonempty text, inspect the shebang, interpreter, dependencies, intended artifacts and the user's existing authorization. Do not assume that saving a script authorizes every action in its body. Apply the user's existing permissions; this tool adds no approval engine.
4. If a temporary script file is needed, create it under `WORKSPACE/.workbox/tmp/`. Use only the **returned script text**, run the chosen interpreter with the workspace root as cwd, and set `WORKBOX_WORKSPACE_ROOT` to that absolute root. The CLI creates no executable script file. Remove only temporary files you created.
5. Verify the exit result **and the promised artifacts**. For the README example, verify `READY.txt` contains exactly `ready` plus a newline. Compare baseline HEADs and working-file state before/after. A printed message or exit zero alone is insufficient. If intended artifacts are unspecified or cannot be checked, report that initialization is not verified.
6. On failure, inspect the retained directory. Within existing authorization, the default allows at most one repair retry with a concrete diagnosis; do not blindly rerun a non-idempotent script. Verify again after repair. Do not silently edit the stored script configuration. If it still fails, stop and report the initialization failure and existing directory. Explicit user authorization can extend investigation; the CLI/DB records no retry counters.

Distinguish `prepared` from initialized: directory preparation can succeed while Agent initialization fails. No initialization status is stored in SQLite or an instance manifest.

## Develop in a native Git worktree

There are no `workbox worktree` commands in this version. Use native Git only after the user specifies the baseline repository, **new** development branch and **new** directory name.

1. Require a single directory name directly below `WORKSPACE/.workbox/worktree/`. Reject nested paths, `.`, `..`, links, escapes and existing entries; validate the container's real path. Do not use a separate/nested workspace.
2. Validate the new branch with Git branch-name rules and verify it does not already exist in the baseline repository. Do not reuse an existing branch.
3. Require the baseline to be clean using `git status --porcelain=v1 --untracked-files=all --ignore-submodules=none`. Reject staged, unstaged, or untracked changes. Do not transfer dirty files.
4. Read `git rev-parse --verify HEAD` and retain the full resolved commit. Immediately before mutation, recheck baseline clean state, HEAD, branch absence, and target conditions. Stop if anything changed.
5. Create from the explicit captured commit, substituting the validated inputs as separate arguments:

   ```text
   git -C BASELINE worktree add -b NEW_BRANCH WORKSPACE/.workbox/worktree/NEW_DIRECTORY FULL_COMMIT
   ```

   Do not substitute `HEAD` or a branch name for `FULL_COMMIT`, fetch, force, reset, reuse a branch or clean/overwrite any existing directory.
6. Verify the new worktree's branch and complete starting commit, and verify the baseline's work files and HEAD remain unchanged. Git's shared refs and administrative records do change; report this accurately. `workbox workspace status --path WORKSPACE --format json` shows both checkouts separately and verifies worktree ownership.

Make development changes and place development outputs in the new worktree. On failure, retain branch/directory evidence and report it; automatic branch/directory removal, old branch reuse, baseline synchronization and arbitrary starting points are outside this MVP.
