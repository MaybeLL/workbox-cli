# Storage backends

Workbox supports SQLite and MySQL through the same configuration-store interface. Backend selection affects configuration storage only; Git clones, workspace directories, the agent skill and uninstall behavior remain local.

## SQLite

Run `workbox init --backend sqlite --skip-skill` (or choose Skill installation). Runtime uses saved settings; `WORKBOX_DB=sqlite` overrides them after the initialization gate. Its file is `config.db` under `WORKBOX_CONFIG_HOME`, otherwise `${XDG_CONFIG_HOME:-~/.config}/workbox`. Relative roots resolve from the current directory. Previous schema-version-1 databases remain compatible.

## MySQL

Use MySQL 8 or later; CI verifies MySQL 8.4 with InnoDB. MariaDB and other servers are not currently verified backends. Have an administrator create an empty database dedicated to Workbox and an application account with `SELECT`, `INSERT`, `UPDATE`, `DELETE` and `CREATE` privileges on that database. The server must allow `GET_LOCK`/`RELEASE_LOCK`. Workbox does not create databases and never needs global administrative privileges.

Initialize with `workbox init --backend mysql --mysql-dsn-file FILE --skip-skill --no-copy-config`, or use `--copy-config` to copy existing configurations. Runtime can override the saved connection using `WORKBOX_DB=mysql` and exactly one connection source:

| Variable | Value |
| --- | --- |
| `WORKBOX_MYSQL_DSN_FILE` | Path to a UTF-8 file containing the driver's DSN; trailing newlines are trimmed. An explicit per-command override; init saves its own immutable copy. |
| `WORKBOX_MYSQL_DSN` | DSN supplied directly in the environment. |

Example private-file contents (replace the host, database, user and password):

```text
workbox:REPLACE_PASSWORD@tcp(db.example.org:3306)/workbox?tls=true
```

Make the file readable only by its owner (`chmod 600 /path/to/private/mysql.dsn`), then select it:

```sh
export WORKBOX_DB=mysql
export WORKBOX_MYSQL_DSN_FILE=/path/to/private/mysql.dsn
workbox init --backend mysql --skip-skill --no-copy-config
workbox workspace-config list
```

`tls=true` verifies the server certificate against system roots and checks its hostname. Configure your organization's CA trust through the operating system when needed. A local disposable test server can use `tls=false`; the driver does not enable TLS by default. Consult the [Go MySQL driver's DSN documentation](https://github.com/go-sql-driver/mysql#dsn-data-source-name) for socket connections and connection options. Do not put credentials in command arguments, repository URLs, initialization scripts or exported configuration packages.

The adapter enforces UTF-8 (`utf8mb4`), autocommit for schema setup, and explicit repeatable-read transactions for configuration operations. Multi-statements and client-side interpolation are disabled. Connection establishment defaults to five seconds, reads/writes to thirty seconds; each store operation has a thirty-second context limit. Failures return sanitized Workbox errors, without raw server diagnostics or DSNs. MySQL failures never silently fall back to SQLite.

Initialization prepares `repo_config`, `workspace_config` and the metadata table `workbox_schema`. Lists/exports against an empty existing database return empty results without creating tables. Unknown versions, unrelated tables or non-InnoDB engines are rejected. Because MySQL DDL commits implicitly, setup uses a named database-specific lock and publishes metadata version 2 after preparing the summary column. An interrupted, recognized version-zero setup remains logically empty; the next valid write completes it. Configuration data changes then run in a separate atomic transaction.

Status, help, version, skill installation and uninstall do not connect to the configuration backend. Uninstall preserves the database, its data and any connection file. Back up and administer a shared MySQL database through your normal database operations. Both backends use the configuration root for `settings.json`; MySQL does not use its `config.db`. See [initialization and recovery](initialization.md).

## Transfer configurations

Selecting a backend never copies data automatically. Existing JSON schema-version-1 packages work with both backends, exclude local UUIDs and connection settings, and include full initialization scripts. For example, after setting your MySQL connection source:

```sh
WORKBOX_DB=sqlite workbox config export --output workbox-config.json
WORKBOX_DB=mysql workbox config import --input workbox-config.json
```

Reverse the backend names to transfer in the other direction. Export creates a new file and refuses to overwrite an existing path. Import validates every referenced remote branch, assigns fresh IDs for new configurations, preserves existing identical configurations and rejects same-name/different-content conflicts. The whole import is atomic. After importing, configure your default environment to select the intended backend. The source store remains unchanged.

## Adapter contract

Business operations depend only on the internal `configStore` interface:

```go
type configStore interface {
    snapshot(context.Context) (snapshot, *Failure)
    commit(ctx context.Context, expected, next snapshot) *Failure
}
```

`snapshot` returns a detached, consistent set of repository and workspace domain values. Reading an empty store does not initialize it. Business code validates names, references, paths, scripts and Git remotes, then prepares the next domain snapshot. `commit` must compare the current snapshot with `expected` inside its write transaction and atomically persist `next`, or return `CONFIG_CONFLICT` without overwriting concurrent changes. A failure before commit rolls back the batch. An ambiguous commit acknowledgement returns `COMMIT_OUTCOME_UNKNOWN`; callers must reread instead of assuming rollback. `commit(s,s)` prepares schema and commits without inventing business records. Network/Git validation stays outside write transactions.

SQLite uses an immediate write transaction; MySQL locks its metadata row before its first consistent data read. Both then compare snapshots and apply changed rows. These locks coordinate Workbox writers; external SQL mutations bypass the adapter contract. SQL helpers are private to the relational adapters, and neither SQL statements, connections, transactions nor driver settings reach business code. The snapshot design fits small configuration catalogs; it is not intended for high-volume operational data.

To add another backend, implement this interface, register selection in the settings/init composition, and add the adapter to the shared storage contract tests. Implement that backend's schema lifecycle, consistency, atomicity and sanitized errors inside the adapter. Business commands and the portable configuration format should not need to change. PostgreSQL and other databases are extension possibilities, not currently supported implementations.

## Summary schema migration

Database schema 2 adds `repo_config.summary` with an empty default. Reads of schema
1 return empty summaries without modifying the database. The next valid write
migrates SQLite transactionally. MySQL serializes its DDL with the existing schema
lock, detects a summary column left by interrupted migration, and publishes schema
2 afterwards. Existing IDs and workspace references are preserved. Older binaries
reject schema 2 instead of silently discarding summaries; upgrade every client
sharing the database before writing. Portable JSON packages remain version 1 and
accept omitted summary fields as empty strings; old tools may reject packages
containing summary fields.

## Team schema migration

Schema 3 adds `team_config` (id, immutable team_key, name, summary) and a JSON
`repo_config.team_ids` column, defaulting to an empty array. Public configuration
uses keys; storage uses local IDs. Schema 1/2 reads do not write or migrate. A valid
write migrates SQLite within its transaction; MySQL serializes resumable DDL and
publishes version 3 last. Its interrupted team table/column creation can be retried.
All database writers use optimistic snapshot checking, including memberships.
Portable packages remain version 1 with optional team_configs and teams fields.
Older clients must not be used after the database is upgraded.
