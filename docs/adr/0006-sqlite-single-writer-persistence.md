# 0006. Persist state in pure-Go SQLite, single writer, WAL

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0004 (workers rely on atomic checkout); [`GoWe-Implementation-Plan.md`](../GoWe-Implementation-Plan.md) performance section

## Context

GoWe's server needs durable state for workflows, submissions, step instances, tasks, and
workers, plus an atomic "claim the next task" operation for the pull model (ADR-0004). The
deployment target ranges from a single binary on a laptop or login node to a small
production server — not (yet) a horizontally-scaled cluster. Two constraints stand out:
build/ops simplicity (this should be a single static Go binary with no external services),
and correctness of concurrent task checkout under many polling workers.

## Decision

We will persist to **SQLite via `modernc.org/sqlite` (pure Go, no CGO)**, configured as a
**single writer** (`SetMaxOpenConns(1)`) in **WAL mode** with foreign keys enforced.
Schema lives in `internal/store/migrations.go` and evolves through idempotent
`ALTER TABLE ADD COLUMN` migrations (`addColumnIfNotExists`). Task checkout MUST be a single
atomic statement over `tasks` (backed by a compound index on `(state, executor_type)`) so
exactly one worker can claim a queued task.

## Consequences

**Positive**
- The server is a single static binary — no CGO, no database server to run or back up
  separately; the store is one file.
- WAL gives concurrent readers alongside the one writer — good for polling workers and the
  scheduler reading state.
- Serializing writes through one connection sidesteps "database is locked" and makes atomic
  checkout straightforward.

**Negative**
- A single writer caps write throughput and means the server does not scale horizontally as
  written — a deliberate ceiling for the current target, not a permanent one.
- SQLite ties durable state to one host's disk; HA/failover would require a different store.

**Neutral**
- Migrating to Postgres later is a bounded change behind the `store` interface — though that
  interface is currently large and a candidate to split (TaskStore/WorkerStore/SubmissionStore).

## Alternatives considered

- **CGO SQLite (`mattn/go-sqlite3`)** — mature, but reintroduces CGO and cross-compilation
  pain. Rejected for the pure-Go driver.
- **Postgres from day one** — scales and offers real concurrency, but adds an external
  service to every deployment including single-node/laptop use. Deferred behind the store
  interface.
- **Embedded KV (bbolt/Badger)** — no SQL, so the atomic conditional checkout and ad-hoc
  queries become hand-rolled. Rejected.

## References

- Code: `internal/store/`, `internal/store/migrations.go`
- Docs: [`GoWe-Implementation-Plan.md`](../GoWe-Implementation-Plan.md) (performance assessment)
