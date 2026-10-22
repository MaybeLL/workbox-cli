# Test contracts and isolation

Use Go modules (not vendor mode). The native Go test backend is sufficient; no xgo mocking is used. Standard assertions, per-test temporary directories and `t.Setenv` isolate tests from user configuration and repositories. Environment-changing tests remain sequential.

Skill installation uses real files under temporary homes and custom skills directories. Tests cover default and Unicode/custom paths, complete embedded references, repeated installs preserving file timestamps, modified/missing/extra content, file/directory/symlink conflicts, invalid arguments, an unwritable parent shape, pre-cancellation and concurrent publication. Conflicts must preserve the entire existing tree, including link targets; concurrent publishers must leave one complete skill and no staging directories. An unusable configuration location and nil Git runner verify that installation needs neither the DB nor Git. No dependency mocks are needed.

Uninstall tests use private executable fixtures, real skill trees and per-test installation receipts. They cover dry-run with no writes, multiple custom paths, historical/default skills, stale records, receipt adoption and concurrent installers, invalid and symlinked receipts, modified/missing/extra/symlinked skill conflicts with no preflight side effects, cancellation and parser errors. A child fake Homebrew process verifies package ownership, the exact uninstall arguments, multiple-version removal and partial failure without invoking the host's Homebrew. A copied real Workbox subprocess verifies successful output after unlinking itself. Configuration, workspace files, unrelated skills and executables must survive.

The installation smoke script repeats real self-uninstall on a disposable copy of the supplied binary, with isolated home, state and configuration. The release workflow separately installs the public Homebrew bottle and invokes the installed executable's uninstall command on an ephemeral macOS runner, verifying the keg, link, skills and receipts are gone while configuration is unchanged. It never uninstalls a maintainer's active executable.

The configuration/CLI tests cover missing-DB reads, strict parser and JSON envelopes, branch/tag validation, save failures preserving content, reference protection, file replacement/clearing, UTF-8 and original script bytes, schema failures, legacy schema compatibility, and configuration-root precedence.

Both SQLite and MySQL run the same storage contract: detached consistent snapshots, preserved IDs and Unicode, stale-write rejection, concurrent first-write serialization, deletion, and rollback after an injected SQL trigger failure. A small test-only memory adapter verifies business commands depend on the storage interface. Backend selection tests cover invalid settings, DSN-file loading and credential redaction; local-only commands work even with an invalid backend setting.

MySQL integration tests require the explicit `WORKBOX_TEST_MYSQL_DSN`, never deployment `WORKBOX_MYSQL_DSN`. Each creates a random, owned database and drops only that database during cleanup. The test account needs database creation/deletion and trigger privileges on a disposable server. CI supplies an ephemeral MySQL 8.4 service. Tests also verify no schema writes on reads, interrupted setup recovery through a real business command, unknown-version/table/engine rejection, scripts exceeding 64 KiB, MySQL/SQLite JSON transfers, real Git workspace creation, and a subprocess using a DSN file. Without the test variable these tests are explicitly skipped; ordinary local checks do not prove MySQL integration.

Workspace agent-entry tests verify that new and empty workspaces receive a regular, non-executable root `AGENTS.md`, plan remains read-only, and no local skill directories or user-global skill installs are created. Real cloned repository rules and clean baselines are preserved. Exclusive file creation rejects files, links and directories without overwriting them; replacement during clone fails with partial output. Case-insensitive targets overlapping root `AGENTS.md` are rejected, including previously saved configurations, before any directory is created.

The workspace tests verify plan makes no directories, create returns only prepared, full-history cloning and actual commits, script handoff without execution, independent plan/create snapshots, in-flight script snapshot consistency, exclusive targets and symlink races, and partial results after second-clone failure or cancellation. The handoff integration test separately executes and checks a declared artifact and verifies baseline HEAD/files remain unchanged.

The import/export tests assert whole-package atomicity, required/unknown/duplicate/null fields, self-contained references, idempotency with fresh remote validation, preservation of local UUIDs, no-overwrite publication, and optimistic conflicts during remote validation.

The status tests inspect real Git indexes, dirty/detached/unborn checkouts, ignored files, initialized submodules, linked worktree ownership and baseline preservation, foreign/broken/symlink/file candidates, unreadable paths and abnormal containers. Unknown fields stay null on failures and readable results survive partial failure.

Git and SQLite use lightweight real implementations in temporary directories. Test-only Git URL rewriting maps HTTPS-shaped URLs to local bare remotes, so branch queries, clones, submodules and worktrees exercise Git without external credentials or network. An injected Git-runner wrapper controls only deliberate process-boundary failures and concurrent DB mutations. Timeout and SIGINT/SIGTERM tests additionally run real subprocesses. Each test owns its files and cleanup; package-global state is limited to the immutable test binary built once before tests.

Build and run the suite:

```sh
go build ./cmd/workbox
go test ./...
go test -race ./...
```

To run the real MySQL suite against a disposable local test server:

```sh
WORKBOX_TEST_MYSQL_DSN='root:TEST_PASSWORD@tcp(127.0.0.1:3306)/?tls=false' \
  go test -race -count=1 -v ./...
```

The installation smoke script forces SQLite and clears inherited MySQL connection settings so it cannot write to a configured deployment database.

## Initialization acceptance

`init_test.go` verifies the settings gate before network/filesystem writes, strict settings and permissions, old SQLite adoption, noninteractive parsing, inactive credential handling, repair with explicit no-copy, stale plans, lock cancellation, and Skill partial failure. MySQL init integration tests use the same isolated database factory and explicit test DSN. Installation smoke runs noninteractive init in each temporary configuration root and verifies settings survive uninstall.

## Terminal interaction

Clack selection tests check retained defaults, navigation, collapsed confirmed answers, cancellation without a false confirmation, and rejection of empty menus. Interactive acceptance should run the built executable in an isolated PTY with temporary HOME/config/state: navigate with arrows, confirm, return to edit a custom path, cancel with Esc and SIGINT, verify hidden password input and restored echo/canonical mode, then repeat with `NO_COLOR`, `TERM=dumb` and a small viewport. Keep JSON/pipe execution noninteractive. This exercises Workbox's terminal driver as well as the Go Clack component model; upstream prompt-runner tests alone are insufficient.
