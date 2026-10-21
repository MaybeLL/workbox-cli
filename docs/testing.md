# Test contracts and isolation

Use Go modules (not vendor mode). The native Go test backend is sufficient; no xgo mocking is used. Standard assertions, per-test temporary directories and `t.Setenv` isolate tests from user configuration and repositories. Environment-changing tests remain sequential.

The configuration/CLI tests cover missing-DB reads, strict parser and JSON envelopes, branch/tag validation, save failures preserving content, reference protection, file replacement/clearing, UTF-8 and original script bytes, schema failures, legacy schema compatibility, and configuration-root precedence.

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
