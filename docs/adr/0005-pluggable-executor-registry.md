# 0005. Dispatch through a pluggable executor registry with ordered selection

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0002 (hints), ADR-0004 (workers), ADR-0007 (BV-BRC); [`Execution-Modes.md`](../Execution-Modes.md)

## Context

The same CWL task must be runnable in very different environments — a local process for
development, a Docker or Apptainer container, a remote pull-worker, or a BV-BRC app service —
without changing the workflow definition. Which environment applies depends on operator
policy, per-step hints, the presence of a `DockerRequirement`, and whether workers are
online. That routing logic must live in exactly one place and be overridable, or it will
scatter across handlers and the scheduler.

## Decision

We will define a single **`Executor` interface** — `Submit`, `Status`, `Cancel`, `Logs` —
and a **registry** of backends implementing it: `local`, `docker`, `apptainer`, `worker`,
`bvbrc`. The scheduler resolves the backend per task by a fixed, documented order (first
match wins):

1. Server `--default-executor` override, if set.
2. `gowe:Execution.executor` hint (`worker` | `bvbrc` | `local`). `container` is *not* a
   routing value — it describes how to run, not where.
3. `gowe:Execution.bvbrc_app_id` ⇒ `bvbrc`.
4. `DockerRequirement` (or `gowe:Execution.docker_image`) ⇒ auto-promote to `worker` when
   workers are online, else run locally.
5. Default ⇒ `local`.

Adding a backend MUST require only a new `Executor` implementation plus registration — no
changes to the scheduler's core loop.

## Consequences

**Positive**
- One uniform contract; the scheduler treats all backends identically.
- Routing precedence is explicit and testable in one function, not emergent.
- New backends (e.g. an HPC/SLURM executor) slot in without touching scheduling.

**Negative**
- The selection order is subtle (the Docker→worker auto-promotion is state-dependent on
  worker availability) and must be documented carefully to avoid surprise.
- A four-method interface is a lowest common denominator; backend-specific capabilities
  (async polling vs synchronous run) leak through `Status`/`Logs` semantics.

**Neutral**
- Sync (`local`, `docker`, `apptainer`) and async (`worker`, `bvbrc`) backends coexist
  behind the same interface; the scheduler's poll phase handles async status.

## Alternatives considered

- **Hard-code executor choice per step in the workflow** — leaks placement into the
  definition and defeats "write once, run anywhere." Rejected (placement stays in hints/policy).
- **One monolithic executor with mode switches** — becomes a god-object; hard to test and
  extend. Rejected in favor of the registry.

## References

- Code: `internal/executor/registry.go`, `internal/executor/executor.go`, `internal/executor/{local,docker,apptainer,worker,bvbrc}.go`
- Docs: [`Execution-Modes.md`](../Execution-Modes.md), [`cwl-hints.md`](../cwl-hints.md)
