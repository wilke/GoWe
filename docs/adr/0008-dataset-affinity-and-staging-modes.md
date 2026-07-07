# 0008. Route by dataset affinity; stage data by mode

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0004 (pull workers), ADR-0002 (hints); [`cwl-hints.md`](../cwl-hints.md), [`Execution-Modes.md`](../Execution-Modes.md)

## Context

Bioinformatics steps depend on large reference data — model weights, sequence databases —
ranging from tens of GB to multi-TB (AlphaFold ~2 TB). Some of it is impractical to move at
task time and is pre-staged on specific workers; some could in principle be fetched but is
far cheaper to reuse where it already exists. Separately, task *outputs* must be placed
somewhere other components can read them, and the right placement differs by deployment:
in-place for a standalone run, a shared filesystem for distributed workers, or an object/
workspace service for BV-BRC. Neither concern belongs in the CWL tool logic.

## Decision

We will express reference-data needs as a **`gowe:ResourceData` hint** listing datasets by
`id` (plus optional `path`, `size`, `mode`, `source`), and route on it in two modes:

- **`prestage`** — the scheduler MUST dispatch only to a worker advertising that dataset;
  the task waits if none is available.
- **`cache`** (default) — the scheduler SHOULD prefer a worker with the dataset but MAY
  dispatch elsewhere.

Workers advertise datasets via `--pre-stage-dir` (auto-scan; dirname = id) or
`--dataset id=path`; `store.CheckoutTask` matches advertised ids against the hint.
Independently, **output staging is a pluggable backend** (`pkg/staging`) selected by URI
scheme — `local` (in-place), `file://` (shared FS), with `http(s)://`, `shock://`, `ws://`
(BV-BRC workspace), and `s3://` defined — supporting copy, symlink, and reference modes.
`--extra-bind` injects arbitrary host paths into containers **without** affecting scheduling.

## Consequences

**Positive**
- Multi-TB datasets that can't be moved are honored declaratively, in the workflow, without
  encoding host layout into tools.
- The same workflow runs standalone, distributed, or on BV-BRC by changing the staging
  backend, not the definition.
- `cache` vs `prestage` lets authors trade strict placement against scheduling flexibility.

**Negative**
- Affinity narrows the pool of eligible workers, so a `prestage` task can starve if its
  worker is busy or offline — a scheduling-fairness edge to watch.
- Dataset ids are a coordination contract between workflow authors and worker operators;
  a typo silently prevents matching.
- Several staging backends (`http`, `shock`, `s3`, `ws`) are defined but not yet fully
  test-covered (see `Execution-Modes.md`).

**Neutral**
- Scheduling affinity (`gowe:ResourceData`) and plumbing (`--extra-bind`) are intentionally
  separate concepts; only the former influences placement.

## Alternatives considered

- **Treat reference data as ordinary CWL `File` inputs** — would try to stage TBs per task
  and lose the "already present, don't move it" semantics. Rejected.
- **A single hard-wired staging mode** — cannot serve laptop, HPC, and BV-BRC deployments
  from one definition. Rejected for the pluggable backend.

## References

- Code: `pkg/staging/`, `internal/store/` (`CheckoutTask` dataset matching), `internal/toolexec/` (binds), `internal/worker/`
- Docs: [`cwl-hints.md`](../cwl-hints.md) (`gowe:ResourceData`), [`Execution-Modes.md`](../Execution-Modes.md)
