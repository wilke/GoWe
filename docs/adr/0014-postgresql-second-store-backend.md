# 0014. Add PostgreSQL as a second store backend

- **Status**: Accepted
- **Date**: 2026-11-25
- **Deciders**: GoWe core
- **Related**: Amends ADR-0006 (SQLite single-writer persistence); ADR-0004 (atomic checkout); [`docs/tools/server.md`](../tools/server.md#postgresql)

## Context

ADR-0006 put all server state in one SQLite file. Some deployments want the state in an
external database instead: to back it up with the tools they already run for their other
databases, and to run live, read-only queries against it (reports, dashboards) without
copying a file off the server host.

SQLite stays the default and fully supported: a laptop, a login node or a small server
should not need a database service. Whatever is added must not change SQLite behaviour.

## Decision

We will support PostgreSQL as a second backend of the one existing store, selected at start.

- **One store, two dialects.** `*store.SQLStore` writes portable SQL with `?` placeholders;
  the parts that differ (placeholders, column types, catalog lookups, JSON access,
  constraint error codes) come from a `dialect` (`internal/store/dialect.go`,
  `internal/store/dialect_postgres.go`). There is no second implementation of the `Store`
  interface.
- **Same logical schema.** The schema in `internal/store/migrations.go` is used on both:
  JSON documents and timestamps stay `TEXT`, booleans stay `0`/`1`, and `INTEGER` columns
  become `BIGINT` on PostgreSQL.
- **One connection.** The PostgreSQL store also uses a single connection
  (`SetMaxOpenConns(1)`), so the single-writer semantics of ADR-0006, which the store's
  transactions and `CheckoutTask` rely on, hold on both backends.
- **Server requirements, checked at start.** PostgreSQL 16 or newer (the JSON helpers use
  `IS JSON`), `UTF8` encoding, and the `C` collation (timestamps and names are `TEXT`
  compared as text, which is only correct in byte order). The server refuses to start
  otherwise.
- **Driver.** pgx v5 through `database/sql` (`github.com/jackc/pgx/v5/stdlib`): pure Go,
  so the server stays a CGO-free single binary.
- **Credentials.** The password MUST come only from `database.password` in the `--config`
  file, which MUST then be a regular file with mode `0600` or stricter. The server refuses
  a password in the URL, the `passfile`, `service`, `servicefile` and `sslpassword` URL
  parameters, and every `PG*` environment variable the driver reads, and ignores `~/.pgpass`
  (`PostgresConfig.PasswordOnlyFromConfig`). The URL and the password are never logged.

## Consequences

**Positive**
- State can live in a managed PostgreSQL, backed up with `pg_dump` and queried live.
- One code path for both backends: a store change is written once, and the store tests
  run on both (`GOWE_TEST_POSTGRES_URL`).

**Negative**
- It is not faster. With one connection, PostgreSQL adds a network round trip per
  statement; throughput is at best that of SQLite.
- Only one server process may use a database. Two servers on one database would break the
  single-writer assumption behind checkout.
- PostgreSQL `TEXT` cannot hold NUL bytes or invalid UTF-8. On PostgreSQL the store removes
  NUL bytes and replaces invalid UTF-8 with U+FFFD in text it writes (task output, for
  example); SQLite stores it unchanged.
- Timestamps are written as fixed-width UTC text (nine fractional digits) on both backends,
  so text order is time order.
- Rows with equal sort keys come back in arbitrary order on PostgreSQL, not insertion
  order. Step instances of one submission are therefore created with timestamps one
  nanosecond apart, so they keep the workflow's step order.
- Known behaviour differences on PostgreSQL:
  - a failed worker lookup at checkout fails the checkout instead of continuing without
    the worker's capacity data;
  - label keys containing a dot match as plain keys, not as a JSON path;
  - a backslash in a search term is an escape character;
  - non-ASCII letters in search terms are matched case-insensitively only on PostgreSQL.
- `cmd/validate-replay` stays SQLite-only.

**Neutral**
- The default (`--db` unset: `~/.gowe/gowe.db`) and every SQLite path, including
  `:memory:`, behave as before.
- Moving an existing SQLite database to PostgreSQL is not provided.

## Alternatives considered

- **A second `Store` implementation for PostgreSQL** — two copies of several thousand lines
  of queries to keep in step. Rejected for the dialect seam.
- **Native `jsonb` and `timestamptz` columns** — better queries on PostgreSQL, but the
  schema, the migrations and the scan code would differ per backend. Rejected for now; the
  `TEXT` columns can be converted later.
- **A connection pool with advisory locks** — would let reads run in parallel, but every
  transaction and the checkout would have to be reviewed for concurrency. Deferred.

## References

- Code: `internal/store/dialect.go`, `internal/store/dialect_postgres.go`,
  `internal/store/postgres.go`, `cmd/server/database.go`
- Docs: [`docs/tools/server.md`](../tools/server.md#postgresql),
  [`docs/PRODUCTION.md`](../PRODUCTION.md)
