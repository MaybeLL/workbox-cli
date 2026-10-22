# Initialize Workbox

Run `workbox init` once after installing or upgrading to a version with initialization support. Existing version-1 databases are adopted without changing their IDs or deleting data. Repository/workspace configuration commands, import/export, and workspace plan/create return `NOT_INITIALIZED` until settings have been published. Help, version, local workspace status, Skill installation and uninstall remain available.

## Interactive setup

In a terminal, run:

```sh
workbox init
```

Choose local SQLite or an existing dedicated MySQL database. First-time setup defaults to SQLite and installing the companion Skill into `~/.agents/skills/workbox`. The path prompt accepts the **parent** Skills directory. MySQL's connection form defaults to port 3306 and verified TLS, and hides password input; alternatively supply a DSN file. Connection failures allow editing/retry. The final menu offers execute, back/edit or cancel. Nothing persistent is written before confirmation.

CLI setup does not clone repositories, create workspace directories or run scripts. Workspace script initialization is a separate Agent/human workflow.

## Scripts and Agents

JSON, pipes and any configuration flags select noninteractive mode. They never wait for terminal input. Specify a backend and exactly one Skill choice:

```sh
workbox init --backend sqlite --install-skill --format json
workbox init --backend sqlite --skip-skill --format json
workbox init --backend mysql --mysql-dsn-file /path/to/private/mysql.dsn \
  --install-skill --skill-path /path/to/skills --no-copy-config --format json
```

These examples are alternatives, not a sequence to run blindly. `--mysql-dsn-file` accepts a regular UTF-8 file (not a leaf symlink) and cannot be combined with either environment DSN source. Input files are never moved or modified. MySQL can instead use one environment credential source, or reuse an existing saved MySQL connection. The database must already exist; Workbox creates its own tables but never the database.

Switches take no `=false` value and cannot repeat. `--skill-path` requires `--install-skill`. With an existing Skill preference, omitted `--skill-path` reuses its parent; otherwise it defaults to `~/.agents/skills`.

## Files and overrides

The configuration root is nonempty `WORKBOX_CONFIG_HOME`, otherwise `$XDG_CONFIG_HOME/workbox`, otherwise `~/.config/workbox`. Relative roots resolve from each invocation's working directory; use absolute paths for persistent environment settings. Existing directory aliases are resolved once per command.

- `settings.json`: strict version-1 settings, publication revision/time, storage choice and Skill preference. This is the only completion record. It contains no passwords or business configurations.
- `config.db`: SQLite business configurations.
- `credentials/mysql-<revision>.dsn`: immutable private MySQL credential snapshots. Changing the input file later does not change saved credentials.
- `init.lock`: kernel lock for concurrent init execution, with a five-second wait limit. File existence alone does not mean init is running.

Created settings/credential files use mode 0600; the credential directory uses 0700. Existing managed files must be ordinary current-user-owned files with no group/other access. Owner-readonly files are accepted. Leaf symlinks are rejected. Old credential revisions are retained, including on uninstall. Skill installation receipts remain under `WORKBOX_STATE_HOME` or the XDG state root.

After initialization, `WORKBOX_DB` overrides the saved backend. MySQL credentials come from exactly one of `WORKBOX_MYSQL_DSN` / `WORKBOX_MYSQL_DSN_FILE`, otherwise from saved MySQL credentials. SQLite ignores MySQL credential variables. DSN-only variables do not switch a saved SQLite backend. Overrides never bypass missing, malformed or future-version settings, and never write back to disk. Only the selected credential source is read.

Init rejects an environment override that would make the saved candidate differ from the next ordinary command's connection. Unset the named conflicting variables and rerun. It never changes the parent shell. Old environment-only MySQL users can first initialize the existing connection, then unset overrides before switching.

## Copy and switch

For different stores with nonempty or unreadable source data, scripts must explicitly choose `--copy-config` or `--no-copy-config`. Interactive copying defaults to no. Explicit no-copy allows repairing an unavailable old connection without connecting to it. The source is always retained.

Copy freezes the source once before confirmation. It includes repository sources, workspace layouts and full scripts, but excludes IDs, credentials, settings, installed Skills and existing workspace files. Every remote branch is verified, so copying can fail offline. The target is reread during execution; new records get new IDs, identical records retain target IDs, and conflicting content rejects the entire batch. Concurrent target changes return `CONFIG_CONFLICT`. Same-database reinitialization does not copy data or revalidate all Git remotes. Source changes after the snapshot are not continuously synchronized.

## Completion and recovery

Init prepares the target schema/transaction, optionally copies configurations, atomically publishes settings, then installs the selected Skill. Settings publication is the completion point. A successful preparation is not a comprehensive test of every future MySQL write privilege.

JSON keeps the `{ok,data,error}` envelope. Inspect `data.state`, `initialized`, `settings.publication`, `storage.preparation`, `copy`, and `skill` together. Unknown or unattempted counts are null. A failed switch may have `initialized: true` because the previous settings remain valid.

- Skill failure after publication: settings remain usable; fix the Skill conflict/path and retry `workbox skill install --path DIR`.
- Target copy succeeded but settings failed: retain the target; retry uses identical-content merging.
- `COMMIT_OUTCOME_UNKNOWN`: the database may have committed. Init stops before publishing settings; restore connectivity and retry after rereading. Never delete target data to simulate rollback.
- Settings rename succeeded but directory sync failed: publication is `unknown`, the command fails, and Skill installation stops. Next invocation uses the actual readable settings.
- Bad/unsupported settings: back up and move only `settings.json` aside before repair, or use a compatible CLI for a future version. There is no force-overwrite option.
- Ctrl+C: return exit 130 and actual phase results when possible. Already-published settings remain. SIGKILL/power loss cannot promise final output.

Settings, database and Skill are separate resources, not one transaction. Partial schema and unreferenced private credential files may remain after failure. Workbox does not delete them automatically. Uninstall preserves settings, every credential revision, databases and workspace files.
