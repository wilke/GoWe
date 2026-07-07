# 0002. Extend CWL via namespaced hints, not a sidecar config

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-09
- **Deciders**: GoWe core
- **Related**: ADR-0001 (CWL adoption), ADR-0005 (executor selection), ADR-0008 (dataset affinity); [`cwl-hints.md`](../cwl-hints.md)

## Context

CWL describes *what* to compute but deliberately says nothing about *where* or *how* to
place work. GoWe needs per-step routing metadata: which executor to use, which worker group
to target, which BV-BRC app backs a tool, which container image to substitute, and which
large datasets a step depends on. This metadata has to travel with the workflow (a separate
sidecar file drifts out of sync and breaks portability), yet it must not break other CWL
engines or make definitions non-portable.

CWL offers two extension surfaces. `requirements` are mandatory — an engine MUST support
them or reject the document. `hints` are advisory — an engine SHOULD support them but MAY
ignore unknown ones. CWL also supports `$namespaces` so custom fields can be prefixed and
unambiguous.

## Decision

We will express all GoWe-specific extensions as **namespaced hints** under
`$namespaces: { gowe: https://github.com/wilke/GoWe# }`, placed in `hints` (not
`requirements`). Two hint objects are defined:

- `gowe:Execution` — executor routing: `executor`, `worker_group`, `bvbrc_app_id`,
  `docker_image`, `gpu`, `inject_bvbrc_token`. **Recognized only in `hints`.**
- `gowe:ResourceData` — dataset affinity (see ADR-0008). Accepted in `hints` or
  `requirements`.

Because they are namespaced hints, other engines (cwltool, Toil) MUST be able to ignore
them safely, keeping the workflow portable. The parser also accepts a legacy `goweHint`
alias for backward compatibility; new files SHOULD use `gowe:Execution`.

## Consequences

**Positive**
- Routing metadata lives with the workflow, versioned alongside it — no sidecar drift.
- Workflows stay runnable under stock CWL engines, which skip unknown hints.
- The extension points are explicit and centralized in one parser path.

**Negative**
- Hints are advisory by CWL's own semantics, so a workflow that *depends* on GoWe routing
  behaves differently (though validly) on another engine — a portability caveat users must
  understand.
- Namespaced YAML keys are more verbose than a bespoke config block.

**Neutral**
- `gowe:Execution` being hints-only (never `requirements`) is a deliberate asymmetry with
  `gowe:ResourceData`; documented in the hints reference.

## Alternatives considered

- **A sidecar `gowe.yaml`** — cleaner CWL, but two files to keep in sync and no single
  portable artifact. Rejected.
- **Put extensions in `requirements`** — would make GoWe workflows fail hard on other
  engines, defeating portability. Rejected.
- **Server-side routing config keyed by step name** — decouples routing from the workflow
  but hides intent from the author and breaks when steps are renamed. Rejected.

## References

- Docs: [`cwl-hints.md`](../cwl-hints.md)
- Code: `internal/parser/parser.go` (`extractStepHints`, `gowe:Execution` / `gowe:ResourceData`), `pkg/model/workflow.go` (`StepHints`)
