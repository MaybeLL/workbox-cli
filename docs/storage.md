# Storage backends

Workbox 0.2 supports SQLite and MySQL through the same configuration-store interface. Backend selection affects configuration storage only; Git clones, workspace directories, the agent skill and uninstall behavior remain local.

## SQLite

No setup is required. An unset/empty `WORKBOX_DB`, or `WORKBOX_DB=sqlite`, selects SQLite. Its file is `config.db` under `WORKBOX_CONFIG_HOME`, otherwise `${XDG_CONFIG_HOME:-~/.config}/workbox`. Relative roots resolve from the current directory. Previous schema-version-1 databases remain compatible.

## MySQL

Use MySQL 8 or later; CI verifies MySQL 8.4 with InnoDB. MariaDB and other servers are not currently verified backends. Have an administrator create an empty database dedicated to Workbox and an application account with `SELECT`, `INSERT`, `UPDATE`, `DELETE` and `CREATE` privileges on that database. The server must allow `GET_LOCK`/`RELEASE_LOCK`. Workbox does not create databases and never needs global administrative privileges.

Set `WORKBOX_DB=mysql` and exactly one connection source:

| Variable | Value |
| --- | --- |
| `WORKBOX_MYSQL_DSN_FILE` | Path to a UTF-8 file containing the driver's DSN; trailing newlines are trimmed. Recommended for persistent configuration. |
| `WORKBOX_MYSQL_DSN` | DSN supplied directly in the environment. |

Example private-file contents (replace the host, database, user and password):

```text
workbox:REPLACE_PASSWORD@tcp(db.example.org:3306)/workbox?tls=true
```

Make the file readable only by its owner (`chmod 600 /path/to/private/mysql.dsn`), then select it:

```sh
export WORKBOX_DB=mysql
export WORKBOX_MYSQL_DSN_FILE=/path/to/private/mysql.dsn
workbox workspace-config list
```

`tls=true` verifies the server certificate against system roots and checks its hostname. Configure your organization's CA trust through the operating system when needed. A local disposable test server can use `tls=false`; the driver does not enable TLS by default. Consult the [Go MySQL driver's DSN documentation](https://github.com/go-sql-driver/mysql#dsn-data-source-name) for socket connections and connection options. Do not put credentials in command arguments, repository URLs, initialization scripts or exported configuration packages.

The adapter enforces UTF-8 (`utf8mb4`), autocommit for schema setup, and explicit repeatable-read transactions for configuration operations. Multi-statements and client-side interpolation are disabled. Connection establishment defaults to five seconds, reads/writes to thirty seconds; each store operation has a thirty-second context limit. Failures return sanitized Workbox errors, without raw server diagnostics or DSNs. MySQL failures never silently fall back to SQLite.

The first valid write creates `repo_config`, `workspace_config` and the metadata table `workbox_schema`. Lists/exports against an empty existing database return empty results without creating tables. Unknown versions, unrelated tables or non-InnoDB engines are rejected. Because MySQL DDL commits implicitly, setup uses a named database-specific lock and publishes metadata version 1 last. An interrupted, recognized version-zero setup remains logically empty; the next valid write completes it. Configuration data changes then run in a separate atomic transaction.

Status, help, version, skill installation and uninstall do not connect to the configuration backend. Uninstall preserves the database, its data and any connection file. Back up and administer a shared MySQL database through your normal database operations. The SQLite configuration-root variables have no effect while MySQL is selected.

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

`snapshot` returns a detached, consistent set of repository and workspace domain values. Reading an empty store does not initialize it. Business code validates names, references, paths, scripts and Git remotes, then prepares the next domain snapshot. `commit` must compare the current snapshot with `expected` inside its write transaction and atomically persist `next`, or return `CONFIG_CONFLICT` without overwriting concurrent changes. A write failure must leave all configuration data unchanged. Network/Git validation stays outside write transactions.

SQLite uses an immediate write transaction; MySQL locks its metadata row before its first consistent data read. Both then compare snapshots and apply changed rows. These locks coordinate Workbox writers; external SQL mutations bypass the adapter contract. SQL helpers are private to the relational adapters, and neither SQL statements, connections, transactions nor driver settings reach business code. The snapshot design fits small configuration catalogs; it is not intended for high-volume operational data.

To add another backend, implement this interface, register selection in `configuredStore`, and add the adapter to the shared storage contract tests. Implement that backend's schema lifecycle, consistency, atomicity and sanitized errors inside the adapter. Business commands and the portable configuration format should not need to change. PostgreSQL and other databases are extension possibilities, not currently supported implementations.
