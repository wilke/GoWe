# 0007. Use one generic BV-BRC executor, not per-app operators

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-09
- **Deciders**: GoWe core
- **Related**: ADR-0001 (CWL), ADR-0005 (executor registry); [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md) ("Operator — Not Needed"), [`BVBRC-API.md`](../BVBRC-API.md)

## Context

GoWe routes some steps to BV-BRC apps (e.g. `GenomeAssembly2`) via JSON-RPC. Airflow-style
frameworks model each external service as a hand-written, typed *Operator* class. That
pattern exists because clouds like AWS/GCP do not expose a single machine-readable schema
for each operation. BV-BRC is different: it self-describes. `AppService.enumerate_apps`
lists apps, and `AppService.query_app_description(app_id)` returns an app's full
`AppParameter[]` schema — types, required flags, defaults, enums, docs — at runtime.

Hard-coding a Go Operator per app would duplicate what BV-BRC already publishes, go stale
whenever BV-BRC changes an app or parameter, require hand-maintaining 20+ definitions, and
limit users to only the apps we had wrapped.

## Decision

We will implement **a single generic `bvbrc` executor** that discovers app schemas
dynamically. For a task carrying `gowe:Execution.bvbrc_app_id`, it MUST:

1. `query_app_description(app_id)` to fetch the live `AppParameter[]` schema (cached with a
   TTL; cache invalidated on validation error).
2. Validate the task's resolved inputs against that schema.
3. Map CWL `File` locations to BV-BRC workspace paths.
4. `start_app(app_id, params, workspace)` and poll `query_tasks` to terminal state.

No per-app Go types are introduced; a new BV-BRC app becomes usable by referencing its
`app_id` in a CWL tool, with no GoWe code change.

## Consequences

**Positive**
- Every BV-BRC app is reachable immediately — no wrapper to write per app.
- Validation is always against the live schema, so it can't drift from the service.
- One executor to maintain instead of a growing operator catalogue.

**Negative**
- Validation and parameter mapping are dynamic, so some errors surface at run time rather
  than compile time.
- Correctness depends on BV-BRC's schema being accurate and available; schema fetches add
  latency and a caching concern (TTL, invalidation).

**Neutral**
- App schemas are a cached, runtime-fetched artifact in the model, distinct from static
  workflow/tool definitions.

## Alternatives considered

- **Per-app typed operators** — compile-time safety, but duplicates BV-BRC's schema, goes
  stale, and caps users to wrapped apps. Rejected (this ADR's core decision).
- **A one-time codegen of tool wrappers from `enumerate_apps`** — avoids hand-writing but
  still snapshots a moving schema and needs regeneration on every BV-BRC change. Rejected in
  favor of live fetch.

## References

- Code: `internal/executor/bvbrc.go`, `internal/bvbrc/`, `pkg/bvbrc/`
- Docs: [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md), [`BVBRC-API.md`](../BVBRC-API.md), [`BVBRC-App-Specs-Summary.md`](../BVBRC-App-Specs-Summary.md)
