# Architecture Decision Records

This directory records the significant architectural decisions behind GoWe — the *why*
behind choices that are otherwise only visible as their consequences in the code.

An **Architecture Decision Record (ADR)** captures a single decision: the context that
forced it, the option chosen, and the consequences accepted. ADRs are immutable once
accepted — when a decision changes, we add a new ADR that supersedes the old one rather
than editing history.

## Status of this log

ADRs 0001–0009 were **back-filled on 2026-07-06** from decisions already embodied in the
codebase and in [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md) and
[`GoWe-Implementation-Plan.md`](../GoWe-Implementation-Plan.md). They document the current
design; they were not written before implementation. New decisions from here on SHOULD be
recorded before or as they are made.

## Index

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-adopt-cwl-v1.2-as-workflow-definition-format.md) | Adopt CWL v1.2 as the workflow definition format | Accepted |
| [0002](0002-extend-cwl-via-namespaced-hints.md) | Extend CWL via namespaced hints, not a sidecar config | Accepted |
| [0003](0003-three-level-state-hierarchy.md) | Model execution as a three-level state hierarchy | Accepted |
| [0004](0004-pull-based-worker-model.md) | Distribute work with a pull-based worker model | Accepted |
| [0005](0005-pluggable-executor-registry.md) | Dispatch through a pluggable executor registry with ordered selection | Accepted |
| [0006](0006-sqlite-single-writer-persistence.md) | Persist state in pure-Go SQLite, single writer, WAL | Accepted |
| [0007](0007-generic-bvbrc-executor-over-operators.md) | Use one generic BV-BRC executor, not per-app operators | Accepted |
| [0008](0008-dataset-affinity-and-staging-modes.md) | Route by dataset affinity; stage data by mode | Accepted |
| [0009](0009-delegated-identity-and-optional-worker-keys.md) | Delegate identity to external providers; gate workers with optional shared keys | Accepted |

## Writing a new ADR

1. Copy [`0000-template.md`](0000-template.md) to `NNNN-short-title.md`, using the next
   free number.
2. Fill in Context → Decision → Consequences. Keep it to one decision.
3. Set the status to `Proposed`; move to `Accepted` once agreed. If it replaces an earlier
   ADR, set that one's status to `Superseded by ADR-NNNN` and link both ways.
4. Add a row to the index above.

## Conventions

- **RFC 2119** keywords (MUST, SHOULD, MAY) carry their normative meaning.
- Reference code by path (e.g. `internal/scheduler/loop.go`) so the record stays anchored
  to the implementation.
- Numbers are permanent and never reused, even if an ADR is later rejected.
