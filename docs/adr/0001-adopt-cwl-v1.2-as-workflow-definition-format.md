# 0001. Adopt CWL v1.2 as the workflow definition format

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-09
- **Deciders**: GoWe core
- **Related**: ADR-0002 (hint extensions), ADR-0007 (BV-BRC executor); [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md) Part 1

## Context

GoWe needs a format for users to describe multi-step scientific computations as a DAG,
targeting BV-BRC apps, containers, and local tools. The realistic options were: invent a
GoWe-native DSL, reuse an existing workflow language (CWL, WDL, Nextflow, Snakemake), or
model workflows only through an API. The target audience is bioinformaticians, many of
whom already write CWL, and a hard requirement is that workflow definitions be *data* —
parseable and validatable without executing user code.

CWL v1.2 fits well: declarative YAML/JSON, data-flow DAG, a real type system for
inputs/outputs, `DockerRequirement` and `ResourceRequirement`, scatter/gather, and
conditional (`when`) execution. It is vendor-neutral with existing parsers, validators,
and a conformance suite. Its gaps for GoWe are that it describes *command-line tools*, not
*service calls*, has no concept of remote job polling, and assumes local file paths.

## Decision

We will use **CWL v1.2 as GoWe's sole workflow and tool definition format**. A workflow is
a `class: Workflow` document; a tool is a `class: CommandLineTool` or
`class: ExpressionTool`. GoWe MUST parse and validate against the CWL v1.2 profile it
supports (see `SPECIFICATION.md`) and MUST track upstream CWL conformance. GoWe's own
concerns — routing, polling, workspace mapping — are kept out of the definition and layered
in via hints (ADR-0002) and the executor/scheduler layers.

## Consequences

**Positive**
- Workflows validate and run locally under `cwltool` before being submitted to GoWe.
- We inherit an existing type system, test suite (378 conformance tests), and user base.
- Definitions carry no execution logic, scheduling, or credentials — clean separation.

**Negative**
- We must implement a substantial CWL executor (bindings, globbing, JS expressions, IWDR,
  secondary files) and keep pace with the spec — a large, ongoing surface.
- CWL's command-line assumption forces workarounds for service-style backends (BV-BRC).

**Neutral**
- Conformance to CWL becomes a first-class, measured obligation (`CONFORMANCE.md`).

## Alternatives considered

- **A GoWe-native DSL** — full control, but no ecosystem, no existing validators, and a new
  language for users to learn. Rejected.
- **WDL / Nextflow / Snakemake** — capable, but less aligned with BV-BRC's container model
  and weaker fit for "definition as inert data validated ahead of run." Rejected.
- **API-only workflow construction** — no portable, inspectable artifact; workflows could
  not be tested outside GoWe. Rejected.

## References

- Docs: [`GoWe-Vocabulary.md`](../GoWe-Vocabulary.md), [`CWL-Specs.md`](../CWL-Specs.md), [`../../CONFORMANCE.md`](../../CONFORMANCE.md)
- Code: `internal/parser/`, `internal/cwltool/`, `internal/cwlexpr/`, `pkg/cwl/`
