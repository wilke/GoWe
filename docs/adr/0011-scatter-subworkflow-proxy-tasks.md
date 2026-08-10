# 0011. Run sub-workflow steps through proxy tasks paired with child submissions

- **Status**: Accepted
- **Date**: 2026-08-09
- **Deciders**: GoWe core
- **Related**: ADR-0003 (three-level state hierarchy), ADR-0004 (pull-based workers),
  ADR-0006 (SQLite single-writer persistence); [`SPECIFICATION.md`](../../SPECIFICATION.md)
  §7.2, §7.4, §8.1; issue #164; PRs #165, #167, #168, #169

## Context

The original sub-workflow implementation (`executeChildSubmission` +
`waitForStepCompletion`) executed each scatter child **inline on the scheduler
goroutine**, one at a time, driving the child's steps to completion in its own nested loop
with no cancellation checks. Issue #164 documented the three user-visible defects:

1. **No parallelism** — a scatter over a sub-workflow ran one child at a time regardless
   of worker count.
2. **Engine-wide head-of-line blocking** — while the inline loop ran, no other submission
   was scheduled; an unrelated submission sat `PENDING, Tasks: 0` for the parent's entire
   duration (observed: 1h+, workers idle).
3. **Uncancellable** — `gowe cancel <parent>` marked the submission CANCELLED but the loop
   kept creating and executing children (observed: 40+ children over 2h after cancel);
   only a server restart stopped it.

The root cause was shared: child execution lived in scheduler memory (an unpersisted
`parentTask`), outside the three state machines the rest of the engine advances, so
nothing could interleave with it, observe a cancel, or recover it after a restart.

## Decision

**Represent each sub-workflow execution as a persisted proxy Task paired 1:1 with a child
Submission, and let the normal tick machinery do everything else.**

- Dispatch creates one proxy Task per scatter combination (`ExecutorType: subworkflow`,
  `ScatterIndex: i`, `Job` = the fully-resolved combination, `MaxRetries: 0`, state
  RUNNING from birth — never QUEUED). A non-scatter sub-workflow is the N = 1 case
  (`ScatterIndex` −1). All proxies plus the step's `READY → DISPATCHED` transition are
  persisted in **one transaction**.
- Each proxy pairs with a child Submission (`ParentTaskID` = proxy id), created
  idempotently after the transaction. Children are ordinary submissions: the same
  scheduler runs their steps, workers pull their tasks, nesting recurses naturally.
- `pollInFlight` advances each RUNNING proxy from its child's state (child COMPLETED ⇒
  proxy SUCCESS with the child's outputs; child FAILED or CANCELLED ⇒ proxy FAILED with
  the child's error in `Stderr`), repairs a missing child from the proxy's persisted
  `Job` (crash between transaction and child creation), and reconciles proxies whose own
  submission or step went terminal. The parent fans in through the existing plain-scatter
  `ScatterIndex` merge in `advanceSteps`.
- Cancellation is a synchronous handler fan-out to all descendants (any depth, reported
  as `children_cancelled`) plus the scheduler's per-tick reconciliation as backstop;
  `CancelNonTerminalTasks` excludes subworkflow proxies so the backstop stays armed, and
  nested cancels cascade one level per tick. Submission and task terminalizations are
  compare-and-set writes (PR #165), and cancel-side step writes touch only non-terminal
  rows, so the two paths race harmlessly.

The 1:1 task↔child pairing is the crux: it reuses all three existing state machines,
makes fan-in a verbatim copy of plain scatter, gives cancel fan-out a natural join key,
and makes recovery per-task idempotent.

### Alternatives considered

- **StepInstance-level tracking without tasks** — store the child submission ids directly
  on the StepInstance and advance it from their states. Rejected: it invents a fourth
  bespoke state ledger next to the three the engine already has; fan-in, failure
  aggregation, cancellation, and the heartbeat kill path all reason in Tasks, so each
  would need a parallel implementation; and there is no per-combination row to serve as
  the idempotency/join key for crash repair and cancel fan-out.
- **Worker-pool inline execution** — keep the inline executor but run children on a pool
  of goroutines inside the server. Rejected: it restores parallelism but keeps execution
  state in memory, so a restart still orphans every in-flight child, cancellation still
  needs bespoke signalling into each goroutine, and it duplicates the scheduler's own
  dispatch/poll/advance machinery instead of reusing it.

## Consequences

**Positive**

- **Durability.** The dispatch transaction plus idempotent per-proxy repair make every
  crash window recoverable: before the transaction, the step re-dispatches cleanly; after
  it, `pollInFlight` recreates missing children from `task.Job` without re-running scatter
  combination or `valueFrom`/`when` JavaScript. A restarted server re-attaches to
  in-flight children instead of re-executing them.
- **Cancellation reaches every level.** Handler fan-out kills descendants at
  cancel-accept time (workers see it on the next heartbeat); the per-tick reconciliation
  catches anything created after the fan-out's snapshot. Live E2E (PR #167): cancel
  cascaded to 14 running children in 0.6 s.
- **Parallelism and no head-of-line blocking.** Children are limited only by worker
  capacity; unrelated submissions dispatch mid-scatter (E2E: 36.2 s vs ~85 s serial for
  6×10 s on 2 workers; 0.5 s dispatch latency for a submission arriving mid-scatter).

**Negative / trade-offs**

- **Listing flood.** Children are real submission rows, so a 64-shard scatter would bury
  the submissions list. Addressed in PR #169: listings exclude children by default
  (`parent_task_id` set) and take `include_children=true` to opt back in; children remain
  individually reachable by id.
- **One-tick latencies.** A proxy advances — and the parent step and submission finalize —
  one tick after the child completes (phases 3→4→5 of the same tick); nested cancels
  reconcile one nesting level per tick (the synchronous handler fan-out hides this for
  the common cancel path).
- A cancelled child surfaces as a FAILED parent step (with the cancellation noted in the
  proxy's `Stderr`), not as a distinct state — the price of keeping fan-in identical to
  plain scatter.
- One `GetChildSubmissions` query per in-flight proxy per tick (N+1); acceptable at
  current scales.

**Neutral**

- `subworkflow` is a scheduler-internal `ExecutorType`, never registered as an executor
  backend (SPECIFICATION §8.1).
- `DELETE /submissions/{id}` now refuses with 409 while descendant children are active,
  since deleting the proxies would sever the cancel cascade.
- **Upgrade note (pre-0.15 → 0.15).** The old code never persisted the step's DISPATCHED
  state, so an in-flight scatter-over-subworkflow submission re-dispatches from scratch on
  upgrade: duplicate children are created, and old orphan children run to completion with
  their results discarded. Cancel such submissions before upgrading; dangling child rows
  can be cleaned up with
  `UPDATE submissions SET state='CANCELLED' WHERE state IN ('PENDING','RUNNING') AND
  parent_task_id != '' AND parent_task_id NOT IN (SELECT id FROM tasks);`
  See [`docs/upgrading.md`](../upgrading.md).
