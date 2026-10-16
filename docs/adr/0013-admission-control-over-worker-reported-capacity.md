# 0013. Admission control over worker-reported capacity

- **Status**: Accepted
- **Date**: 2026-10-16
- **Deciders**: GoWe core
- **Related**: ADR-0004 (pull-based workers), ADR-0008 (dataset affinity); [`SPECIFICATION.md`](../../SPECIFICATION.md) §4.1; issue #324

## Context

`CheckoutTask` gave any eligible task to any worker in the group. Two gaps followed:

1. `ResourceRequirement` expressions (`coresMin: $(inputs.threads)`) were invisible to the
   server: `RuntimeHints.Cores/RamMB` stayed 0, so nothing could reason about them.
2. A task whose `ramMin` exceeded every worker's cap was checked out anyway and failed in
   `cwltool.ResolveResources` ("task requires N MiB RAM but worker max is M MiB") after
   burning all its retries across workers.

## Decision

Admit a task to a worker only if it fits the capacity that worker reported.

- **Evaluate at dispatch.** The scheduler evaluates the final merged tool's
  `ResourceRequirement` (coresMin/cores/ramMin, expressions included) against the task's job
  with the same rules as `ResolveResources` (`cwltool.EvaluateResourceRequest`) and stores
  the result in `RuntimeHints.Cores/RamMB`, per scatter iteration. A value that cannot be
  evaluated server-side (e.g. depends on `File.size`) stays 0 and logs one WARN; dispatch
  never fails because of it.
- **Report capacity.** Workers send their effective `max_cpus` / `max_mem_mb` at
  registration; stored in nullable `workers` columns. NULL means legacy or undetected.
- **Gate checkout.** `CheckoutTask` loads the worker row first and filters tasks with
  `cores > max_cpus` or `ram_mb > max_mem_mb` inside the candidate query, so an unfit task
  at the head of the queue cannot fill the fetch window and starve fitting tasks behind it.
  A NULL capacity disables the check for that dimension.
- **Explain.** `WorkerCapabilities` carries the largest reported capacity per group;
  `canMatchTask` and stuck-task detection name the gap ("no online worker in group G has >=
  N MiB (largest M MiB)").

## Consequences

**Positive**
- Tasks larger than every worker in their group stay `QUEUED` and are reported (preflight,
  stuck detector) with a capacity reason instead of failing after three checkouts.
- Step-level `ResourceRequirement` now reaches the hints with correct CWL precedence.

**Negative**
- Capacity is self-reported and trusted, the same trust class as `--max-cpus` today.
- Requirement keys for stuck detection now include cores/RAM, so tasks differing only in
  those values are tracked separately.
- The dispatch preflight resolves the task's effective worker group (tool hint, step hint,
  `--group` label) itself, because the template task it checks carries none. Capacity is
  judged against that group only while it has online workers; an offline group keeps the
  pre-existing preflight semantics (dispatch, queue, stuck-task detector) since fit cannot be
  judged without its workers. Scatter steps are checked per iteration before any task is
  created. Untagged tasks are judged against the default group, the only one that serves them.

**Neutral**
- Shared-host budgeting (several workers on one machine) is out of scope here and is
  tracked as follow-up work on #324.

## Alternatives considered

- **Check only on the worker (status quo)** — burns retries and gives no queue-side signal.
- **Server-side capacity inferred from running tasks** — needs a host identity the worker
  does not yet report; deferred.
