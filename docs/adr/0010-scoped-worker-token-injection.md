# 0010. Scope automatic delegated-token injection to trusted worker groups

- **Status**: Accepted
- **Date**: 2026-07-07
- **Deciders**: GoWe core
- **Related**: ADR-0009 (delegated identity), ADR-0007 (BV-BRC executor); [`SPECIFICATION.md`](../../SPECIFICATION.md) §13.1, §13.5; PR #132, issue #133

## Context

ADR-0009 established that the submitter's own BV-BRC token is injected into a task's
container **only** when the step opts in with `gowe:Execution.inject_bvbrc_token`, so a job
runs under the user's identity. In practice, BV-BRC tool workers run curated Perl tooling
(AppScript, the Workspace client) that expects `BVBRC_TOKEN` / `KB_AUTH_TOKEN` in the
environment for *every* step. Requiring the `inject_bvbrc_token` hint on every tool in every
workflow was error-prone friction, and the first external contribution (PR #132) proposed
dropping the gate entirely — injecting the token into **all** worker tasks and adding the
`KB_AUTH_TOKEN` alias.

Removing the gate unconditionally is a confused-deputy risk: a workflow that runs any
third-party or public tool container would then receive the user's live bearer credential and
could exfiltrate it. §13.5 named this exact change and required that adopting it be recorded in
an ADR with the least-privilege trade-off made explicit.

## Decision

**Keep least-privilege as the default; let operators opt specific worker groups into
automatic injection.**

The submitter's token is injected (as `BVBRC_TOKEN` and its `KB_AUTH_TOKEN` alias) when, and
only when, at least one of these holds:

1. the step opts in with `gowe:Execution.inject_bvbrc_token`;
2. the task targets the BV-BRC executor;
3. a workspace stager requires the token to move `ws://` data; or
4. the task's worker group appears in the server's `--token-inject-groups` allowlist.

`--token-inject-groups` is empty by default, so out of the box only the explicit per-tool
opt-in (case 1) and the pre-existing executor/staging cases apply — no behavior change for
existing deployments. An operator running curated BV-BRC tool workers sets
`--token-inject-groups bvbrc` (or their group name) to grant those workers automatic
injection, while the default group and any unlisted group stay opt-in.

Two supporting guarantees make this safe:

- **No log leakage.** Because the worker now captures tool stdout/stderr for reporting, it
  redacts every injected secret value from the captured buffers before they leave the worker,
  upholding the §13.2 trust-boundary invariant even if a tool echoes its environment.
- **No lingering credential.** The injected token is scrubbed from the task's persisted
  runtime hints once the task reaches a terminal state (in addition to being encrypted at rest
  per ADR/§13.5).

## Consequences

**Positive**
- BV-BRC tool workers get the token automatically without per-tool hints, removing the friction
  PR #132 targeted.
- Least privilege is preserved by default: untrusted/default-group tools never see the token
  unless a workflow explicitly asks.
- The trust boundary holds even with the new log capture, thanks to redaction.

**Negative / trade-offs**
- Operators who set `--token-inject-groups` are asserting that every tool run on those groups
  is trusted with the user's credential; a malicious or compromised tool image scheduled to a
  listed group could read the token. This is a deliberate, operator-scoped trust decision, not
  a global default.
- Group-based scoping is coarser than per-tool opt-in; a listed group applies to all tasks
  routed there.

**Neutral**
- The `inject_bvbrc_token` hint remains fully supported and is still the right mechanism for
  one-off delegated tools on otherwise-untrusted groups.
