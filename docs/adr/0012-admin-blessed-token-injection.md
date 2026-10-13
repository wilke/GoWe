# 0012. Admin-blessed token injection

- **Status**: Accepted (supersedes ADR-0010)
- **Date**: 2026-10-12
- **Deciders**: GoWe core
- **Related**: ADR-0009, ADR-0010; [`SPECIFICATION.md`](../../SPECIFICATION.md) §5.1, §13.5;
  [`docs/security/tool-blessing.md`](../security/tool-blessing.md); PRs #288, #290, #291, #292

## Context

The submitter's BV-BRC token is a full-account credential with no audience claim and no
upstream revocation. ADR-0010 let a step opt in, or an operator-designated worker group
receive it automatically. Review found that a step hint could replace the tool's hints, that
job `cwl:requirements` and workflow/step requirements could change what a tool runs after it
was reviewed, and that passthrough staging put the token in every task row.

## Decision

1. **Content-bound blessing.** An administrator blesses a tool by the digest of its final
   post-merge document (requirements and hints, benign inherited classes stripped, default-deny
   for the rest) plus a **list of pinned image sha256 digests**. No expiry; revocation is explicit.
2. **Tool-only request.** `inject_bvbrc_token` is read from the tool document's `hints`. Steps may
   route (executor, worker group, gpu, secret opt-ins) but never request; `gowe:Execution` under
   `requirements` is rejected at registration.
3. **Fail fast at submit, authoritative at dispatch.** Submission is refused (`TOOL_NOT_BLESSED`,
   `TOKEN_TOOL_OVERRIDE_REFUSED`) using the same digest code on the real job requirements and the
   sub-workflow child root. The scheduler recomputes the digest of `task.Tool`, requires the worker
   executor and an allowed group, and is the only writer of the grant fields (`attachCredentials`;
   enforced by a go/types meta-test and a store guard). Retries re-run the whole policy.
4. **Staging credential is separate and need-based**: a worker task carries the token only for
   `ws://`/`shock://` inputs or an output destination, never causing injection. `--token-inject-groups`
   is staging-only and deprecated.
5. **Dev mode** `--token-blessing=dev`: unblessed requests are granted (image digest computed
   server-side from `--dev-image-dir`), loudly announced; nothing else is relaxed. Default `enforce`.
6. BV-BRC executor tasks are unchanged (header only); a token request there is a no-op.

## Consequences

Operators must bless the helper tools before using them, and re-bless on any content or image
change. Workflows can no longer smuggle requirements into a blessed tool. Residuals: value-based
output redaction, blessing revocation re-checked only on real retry, SQL requeue bypasses credential
attachment.
