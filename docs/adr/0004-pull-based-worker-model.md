# 0004. Distribute work with a pull-based worker model

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0005 (executor registry), ADR-0006 (store), ADR-0008 (dataset affinity); [`Remote-Worker-Analysis.md`](../Remote-Worker-Analysis.md)

## Context

GoWe runs work on remote machines that the server does not control and often cannot reach:
HPC login/compute nodes, GPU boxes behind NAT or firewalls, lab workstations. Those hosts
have heterogeneous capabilities (Docker vs Apptainer vs none, GPUs, pre-staged multi-TB
datasets). A push model requires the server to open connections *to* each worker, hold a
registry of reachable addresses, and know each worker's live capacity — all of which break
down when workers are unaddressable or come and go.

## Decision

We will use a **pull model: workers poll the server for work; the server never pushes**.
A worker registers, then loops:

1. `GET /api/v1/workers/{id}/work` — request the next matching task (HTTP 204 = none).
2. Execute via `internal/toolexec` (mounts, GPU, injected secrets that MUST NOT be sent to
   the server).
3. `PUT …/tasks/{tid}/status` then `PUT …/tasks/{tid}/complete` — report progress and result.
4. `PUT /api/v1/workers/{id}/heartbeat` — periodic liveness.

Task claiming is a server-side atomic checkout (`store.CheckoutTask`) that matches on
container-runtime capability, worker group, and dataset affinity (ADR-0008) and flips the
task to `RUNNING` for exactly one worker. Workers authenticate with an `X-Worker-Key`.
The scheduler MUST detect stalled claims (missed heartbeats, zero-progress `QUEUED` tasks)
and return them for re-dispatch.

## Consequences

**Positive**
- Workers behind NAT/firewalls need only outbound HTTPS; no inbound reachability required.
- Horizontal scaling is trivial — start more workers; they self-select matching work.
- Capability/dataset matching happens at claim time against ground truth the worker reports.

**Negative**
- Polling adds latency (a task waits up to one poll interval, ~5 s) and steady request load.
- Liveness is inferred from heartbeats, so failure detection is delayed and needs a
  stuck-task reaper to avoid orphaned claims.
- Exactly-once dispatch depends on the atomicity of the checkout query.

**Neutral**
- The server becomes the single source of truth and the only stateful component; workers are
  stateless and disposable.

## Alternatives considered

- **Server pushes to workers** — requires addressable, reachable workers and server-held
  capacity tracking; fails for HPC/NAT. Rejected.
- **A message broker (NATS/RabbitMQ/Redis)** — solves distribution but adds an operational
  dependency and still needs capability-aware routing; overkill at current scale. Rejected.

## References

- Code: `internal/worker/`, `internal/server/handler_workers.go`, `internal/store/` (`CheckoutTask`), `internal/toolexec/`
- Docs: [`Remote-Worker-Analysis.md`](../Remote-Worker-Analysis.md), [`Execution-Modes.md`](../Execution-Modes.md)
