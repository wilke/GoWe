# gowe-db-copy

`gowe-db-copy` copies a GoWe SQLite database into an empty PostgreSQL database, so that a deployment can move
from SQLite to PostgreSQL and keep its history (workflows, submissions, tasks with their logs, workers, worker
keys, tool blessings, users, sessions and the label vocabulary). It is a one-off tool: it runs once, offline, on
a copy of the SQLite file.

There is no tool for the other direction (PostgreSQL back to SQLite). The way back is the SQLite file you copied
from: keep it.

## Installation

```bash
# From source
go build -o gowe-db-copy ./cmd/db-copy
```

The tool is built from source only; it is not part of the release archives.

## Usage

```bash
gowe-db-copy --from <sqlite file> [--to postgres://...] [--config server.json] [--migrate-source]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--from` | (required) | The SQLite database file to copy. Must be a regular file: a copy, not the live database |
| `--to` | `""` | The PostgreSQL target, a `postgres://` or `postgresql://` URL **without a password**. Default: `database.url` of `--config` |
| `--config` | `""` | The server config file (JSON). Its `database` block supplies `url`, `password` and `schema`, exactly as for `gowe-server` (the server additionally refuses unknown top-level keys in this file; this tool ignores them) |
| `--migrate-source` | `false` | Bring the SQLite file to the current schema before copying. This **writes to the file** |
| `--debug` | `false` | Debug logging (the default level is WARN) |

The target is selected with the same rules as the server's database (see
[server.md](server.md#postgresql)): `--to` plays the role of `--db`, the password may only come from
`database.password` in a mode 0600 `--config` file, and the tool refuses a password in the URL, the `passfile=`,
`service=`, `servicefile=` and `sslpassword=` parameters, and any `PG*` environment variable the driver would
read. The target must be PostgreSQL; a SQLite target is refused.

The URL and the password are never printed.

## What it does

1. Opens the SQLite file read-only (`mode=ro`: the file itself is never written). If the file is in WAL mode,
   SQLite may create `-shm` and `-wal` files next to it, so the directory must be writable. With
   `--migrate-source` it opens the file read-write and runs the schema migration first, printing
   `migrating the schema of <file> (this modifies the file)`.
2. Opens the PostgreSQL database (checking version, UTF8 encoding and C collation, as the server does) and creates
   the schema there if it is not there yet.
3. Checks that every table has the same columns in both databases. A SQLite file from an older GoWe version lacks
   the newest columns; the tool then stops and names the table and the missing columns. Run it again with
   `--migrate-source` on a copy. A table in the SQLite file that is not part of the GoWe schema is refused too,
   rather than left behind.
4. Checks that the target is empty: every GoWe table has no rows. Otherwise it stops with
   `target database is not empty (table X has N rows)`.
5. Copies every table, row by row in primary key order, in **one** PostgreSQL transaction.
6. Reads both databases again, in primary key order, and compares every column of every row.
7. Commits only if everything matched.

It is all-or-nothing: on any error, a verification difference, or Ctrl-C, the transaction is rolled back and no
row is written to PostgreSQL (the empty tables created in step 2 stay). Errors name tables, columns, row keys and
counts, never the values in a row (they can hold tokens and secrets); rows of `sessions` are identified by their
position only, because the session id is the login cookie.

### Values that change on the way

The tables and columns are the same in both databases; three kinds of value are rewritten:

- **Timestamps.** TEXT timestamps written by older versions have a variable width (`2026-03-01T10:00:05Z`); they are
  rewritten in the fixed-width UTC form current versions write (`2026-03-01T10:00:05.000000000Z`), which sorts
  correctly as text. Same instant, nothing lost. A value that is not a timestamp is copied unchanged.
- **Text PostgreSQL cannot store.** PostgreSQL text cannot hold NUL bytes or invalid UTF-8, which SQLite stores (a
  task's stdout or stderr can contain both). NUL bytes are removed and invalid UTF-8 is replaced with U+FFFD, the
  same rule the server applies when it writes to PostgreSQL.
- **JSON documents the server queries** (`tasks.runtime_hints`, `workflows.labels`): the JSON escape for a NUL
  character (`\u0000`) is replaced by the escape for U+FFFD, since PostgreSQL cannot read it in a JSON value.

Everything else is copied as it is, including the encrypted provider tokens and secrets: they can only be
decrypted with the same token key as before.

## Procedure

1. **Stop the server** (or drain it and stop it), so that the SQLite file no longer changes.
2. **Copy the SQLite file** with SQLite's online backup, which gives a consistent file even if the server had not
   checkpointed its write-ahead log:

   ```bash
   sqlite3 'file:/path/gowe.db?mode=ro' ".backup /path/copy.db"
   ```

   For a trial run while the server keeps running, do not use `.backup`: it starts over every time the server
   writes and may never finish. Take one read snapshot instead:

   ```bash
   sqlite3 'file:/path/gowe.db?mode=ro' "VACUUM INTO '/path/copy.db'"
   ```

   Both commands only read the server's file.

3. **Create the PostgreSQL database** with UTF8 encoding and the C collation, and optionally a schema, as described
   in [server.md](server.md#postgresql).
4. **Write the config file** the server will use, with the password, and make it mode 0600:

   ```json
   {
     "database": {
       "url": "postgres://gowe@127.0.0.1:5433/gowe",
       "password": "...",
       "schema": "gowe"
     }
   }
   ```

   ```bash
   chmod 600 /etc/gowe/server.json
   ```

5. **Run the tool** on the copy. Add `--migrate-source` if the copy comes from an older GoWe version (the tool
   tells you when it is needed):

   ```bash
   gowe-db-copy --from /path/copy.db --config /etc/gowe/server.json
   # or, from an older version:
   gowe-db-copy --from /path/copy.db --config /etc/gowe/server.json --migrate-source
   ```

   `--migrate-source` refuses a file that has a non-empty `-wal` file next to it, because that looks like a live
   database: copy it first.

6. **Start the server on PostgreSQL with the same token key** as before (`--token-key-file`, or `GOWE_TOKEN_KEY`):

   ```bash
   gowe-server --config /etc/gowe/server.json --token-key-file /etc/gowe/token.key ...
   ```

   With another key or none, the copied provider tokens and secrets cannot be decrypted.

7. **Keep the SQLite file** (the original and the copy). It is the rollback: start the server again with
   `--db /path/gowe.db` to go back. Work done on PostgreSQL in the meantime is not carried back.

## Things to know

- **Do not start a server on the PostgreSQL database until the tool has finished.** The tool locks the tables
  for the duration of the copy; a server started meanwhile waits and then finds the copied data.
- **`--migrate-source` changes the file it is given.** It refuses a file with a non-empty `-wal` next to it (a
  running server), but it cannot tell a copy from the real database of a stopped server. Migrating the real
  file is what a newer server does at start anyway; the consequence is that an older `gowe-server` binary may
  no longer be the right rollback for that file. Run it on a copy.
- **"SQLSTATE 23503"** means a row refers to a row that does not exist, for example a `linked_providers` entry
  whose user was deleted. SQLite allowed it; PostgreSQL does not. Delete the orphan row in the copy and run the
  tool again.
- **If the tool reports that the commit was not confirmed**, the connection failed at the last step and the rows
  may or may not be there. Look at the target before retrying: the tool refuses a database that is not empty.
- **The target is migrated before it is checked.** A PostgreSQL database that already holds GoWe data from an
  older version is brought to the current schema and then refused because it is not empty.

## Output

Progress goes to stderr, one line per table and every 1000 rows:

```
copying /path/copy.db into PostgreSQL (one transaction, verified before commit)
  users: 12 rows
  linked_providers: 3 rows
  workflows: 240 rows
  ...
```

The report goes to stdout:

```
table                  rows   timestamps fixed  text sanitised
users                  12     0                 0
linked_providers       3      0                 0
workflows              240    240               0
submissions            5120   9876              0
step_instances         20480  30122             0
tasks                  40961  120544            17
workers                8      16                0
worker_keys            4      4                 0
blessed_tools          2      2                 0
worker_group_policies  3      0                 0
server_settings        1      0                 0
sessions               30     0                 0
label_vocabulary       6      6                 0
copy verified: 66870 rows in 13 tables

Reminder: start gowe-server on PostgreSQL with the same token key (--token-key-file or GOWE_TOKEN_KEY)
as before: encrypted provider tokens and secrets were copied as they are and only that key decrypts them.
Keep the SQLite file: it is the way back.
```

| Column | Meaning |
|--------|---------|
| `rows` | Rows copied (and verified) |
| `timestamps fixed` | TEXT timestamps rewritten in the fixed-width UTC form (each value counts once) |
| `text sanitised` | Values changed because PostgreSQL cannot store them as written: NUL bytes removed, invalid UTF-8 replaced, or the JSON NUL escape replaced |

`copy verified` is printed only after the commit.

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Copied, verified and committed |
| 1 | The copy failed or did not verify; nothing was committed |
| 2 | Invalid flags, `--from` missing or not a regular file, the target is not PostgreSQL, or `--migrate-source` next to a non-empty `-wal` file |
