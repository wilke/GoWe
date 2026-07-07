# 0003. Model execution as a three-level state hierarchy

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0004 (workers), ADR-0005 (executors); [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md) Parts 2–3

## Context

A run has to track state at several granularities at once. A user submits one workflow and
wants a single answer ("did my run succeed?"). But a scatter step fans out into many
parallel units, each of which can independently succeed, fail, or be retried, and each unit
may map to more than one concrete execution attempt. Collapsing all of this into one flat
list of "tasks" loses the step boundary (needed for dependency resolution and `when`
skipping) and the run boundary (needed for finalization); collapsing it into two levels
forces scatter fan-out and retry to share a state machine, which muddies both.

## Decision

We will model execution as **three nested entities, each with its own state machine**:

- **Submission** — one run of a workflow. `PENDING → RUNNING → COMPLETED | FAILED | CANCELLED`.
- **StepInstance** — one instance of a step within a submission; a scatter step produces **N**
  instances. `WAITING → READY → DISPATCHED → RUNNING → COMPLETED | FAILED | SKIPPED`.
- **Task** — the concrete unit of work handed to an executor.
  `PENDING → SCHEDULED → QUEUED → RUNNING → SUCCESS | FAILED | SKIPPED`, with
  `FAILED → RETRYING → QUEUED` for retries.

State flows strictly upward: Task terminality drives StepInstance advancement, and
StepInstance terminality drives Submission finalization. Each entity's valid transitions
are enforced in `pkg/model` (`state.go`, `step_instance.go`, `task.go`); illegal
transitions MUST be rejected.

## Consequences

**Positive**
- Scatter is expressed cleanly: N StepInstances, each owning its Tasks — no special-casing.
- Dependency resolution and `when`-skipping operate at the StepInstance level, where the
  DAG edges actually live.
- Retries live at the Task level and don't perturb step- or run-level state.

**Negative**
- Three state machines and their cross-level transition rules are more to implement, test,
  and keep consistent than a flat model.
- Aggregating Task/StepInstance state up to a correct Submission verdict is non-trivial
  (e.g. "all instances terminal, any failed with no retries ⇒ FAILED").

**Neutral**
- Terminology is fixed and normative (Submission / StepInstance / Task); "job", "run", and
  "pipeline" are disambiguated in the vocabulary.

## Alternatives considered

- **Flat Task list per submission** — simplest, but no home for the step boundary or scatter
  grouping; dependency and conditional logic become fragile. Rejected.
- **Two levels (Submission + Task)** — forces scatter fan-out and retry attempts to share
  one state machine. Rejected in favor of an explicit StepInstance layer.

## References

- Code: `pkg/model/state.go`, `pkg/model/submission.go`, `pkg/model/step_instance.go`, `pkg/model/task.go`
- Docs: [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md)
