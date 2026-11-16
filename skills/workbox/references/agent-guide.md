# Agent handoff and development guide

`workbox` prepares directories and returns script text. The Agent is responsible for initialization and subsequent development. Commands support JSON envelopes; check `ok`, the process exit code and partial results rather than relying on printed success text.

Before storage-dependent commands, complete CLI initialization using the `SKILL.md` instructions. A successful `workbox init` configures storage; it does not execute Workbox initialization scripts.

## Initialize a prepared Workbox

1. Use the original create response if this instance has already been prepared. For a new instance call `workbox create --config NAME --path DIR --format json`. Continue only if it returns `ok: true` and `data.state: "prepared"`. Retain this response's `workspace_path`, repository list, and `init_script`. Do not call `show` to choose a possibly newer script for this create.
2. For scripts needing local source, confirm the user has requested the relevant checkouts before running them. For each existing baseline, inspect its HEAD and `git status --porcelain=v1 --untracked-files=all --ignore-submodules=none`. Record the results. The baseline's working files and HEAD must not be changed by initialization or development.
3. For empty script text, there are no additional script tasks. For nonempty text, inspect the shebang, interpreter, dependencies, intended artifacts and the user's existing authorization. Do not assume that saving a script authorizes every action in its body. Apply the user's existing permissions; this tool adds no approval engine.
4. If a temporary script file is needed, create it under `INSTANCE_DIR/.workbox/tmp/`. Use only the **returned script text**, run the chosen interpreter with the Workbox root as cwd, and set `WORKBOX_WORKSPACE_ROOT` to that absolute root. The CLI creates no executable script file. Remove only temporary files you created.
5. Verify the exit result **and the promised artifacts**. For the README example, verify `READY.txt` contains exactly `ready` plus a newline. Compare baseline HEADs and working-file state before/after. A printed message or exit zero alone is insufficient. If intended artifacts are unspecified or cannot be checked, report that initialization is not verified.
6. On failure, inspect the retained directory. Within existing authorization, the default allows at most one repair retry with a concrete diagnosis; do not blindly rerun a non-idempotent script. Verify again after repair. Do not silently edit the stored script configuration. If it still fails, stop and report the initialization failure and existing directory. Explicit user authorization can extend investigation; the CLI/DB records no retry counters.

Distinguish `prepared` from initialized: directory preparation can succeed while Agent initialization fails. No initialization status is stored in SQLite or an instance manifest.

## Develop in a native Git worktree

Use native Git under the same instance. A request for a worktree includes obtaining the missing baseline checkout at the configured target_dir. Keep the baseline's working files and HEAD unchanged.

1. Identify the selected repository and starting commit. When analysis used a moving branch and recorded commit A, query only its tip with `workbox resolve-ref --path DIR --repo NAME --ref BRANCH --format json`. If it now points to B, report the repository, branch, A and B; wait for the user's choice before reading B's source or creating a worktree. For B, verify the affected conclusions against B before developing. For A, develop directly from A. A configured fixed commit is already a concrete baseline. If the tip or analysis SHA is unknown, report the gap instead of claiming the commits match.
2. For the new branch and directory, honor each explicit name, then repository conventions, then choose an unused task-derived name. Check Git branch validity, branch existence and destination availability. Auto-generated collisions get a fresh name; for a collision with a user-specified name explain and ask. Use a single directory name directly under the real `.workbox/worktree/`. Reject symlinks, escapes and existing entries.
3. Verify baseline cleanliness (including untracked files) and capture its HEAD and working-file state. If the selected commit object is missing, fetch the necessary object using the configured repository without switching or resetting the baseline. If unavailable, report the failure. Resolve the selected commit to its full SHA. Immediately before mutation recheck the baseline and destination.
4. Run `git -C BASELINE worktree add -b NEW_BRANCH INSTANCE_DIR/.workbox/worktree/NEW_DIRECTORY FULL_COMMIT`. Verify the new branch and commit, confirm the baseline's HEAD and files are intact, and report the branch and full path. `workbox status --path INSTANCE_DIR` inspects both checkouts. On failure retain any existing branch or directory and report its state.
