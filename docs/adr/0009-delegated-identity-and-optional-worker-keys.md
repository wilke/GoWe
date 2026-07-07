# 0009. Delegate identity to external providers; gate workers with optional shared keys

- **Status**: Accepted (back-filled 2026-07-06)
- **Date**: 2026-02-18
- **Deciders**: GoWe core
- **Related**: ADR-0001 (CWL), ADR-0004 (pull workers), ADR-0007 (BV-BRC); [`SPECIFICATION.md`](../../SPECIFICATION.md) §13

## Context

GoWe serves BV-BRC and MG-RAST users who already hold accounts and tokens with those
services. The engine has to (a) authenticate API callers, (b) authorize a small set of admin
actions, and (c) let remote pull-workers (ADR-0004) join from HPC/NAT networks — without
standing up its own password store or identity provider. It also has to submit BV-BRC jobs
*as the requesting user*, not as a shared service account (ADR-0007), which means the user's
own credential must reach the BV-BRC executor. Two questions fall out: how do end users
authenticate, and how do workers authenticate.

## Decision

**Delegate user identity; keep worker auth simple and optional.**

- **Users** present a provider-issued, pipe-delimited token — BV-BRC via `Authorization`,
  MG-RAST via `X-MG-RAST-Token`. The server parses the token for username and expiry, rejects
  empty/expired tokens, and **auto-provisions** a local user record keyed by
  `(username, provider)`. GoWe stores **no passwords**. Roles are `user` / `admin` /
  `anonymous`; admin membership comes from config (stored role, `--admins`, `GOWE_ADMINS`,
  config file). Anonymous access is **opt-in** (`--allow-anonymous`) and MUST be scoped by
  `--anonymous-executors`.
- **Workers** authenticate with an **optional** shared `X-Worker-Key` that maps to a set of
  allowed groups; if no keys are configured, worker endpoints are open. The key is never
  logged in the clear (hash only).
- **Delegation**: when `gowe:Execution.inject_bvbrc_token` is set, the submitter's own token
  is injected into the task container as `BVBRC_TOKEN`, so downstream BV-BRC work runs under
  the user's identity.

## Consequences

**Positive**
- No identity provider, password store, or credential-reset flow to build or operate.
- Users reuse the credentials they already have; onboarding is zero-config.
- Workers join with a single shared secret (or none in a trusted network) — no PKI to stand up.
- BV-BRC jobs are correctly attributed to the requesting user.

**Negative**
- Trust is only as strong as the provider token and our validation: GoWe **parses** the token
  for username/expiry but does not cryptographically verify a provider signature, so it relies
  on transport security and provider issuance.
- The shared worker key is coarse — no per-worker identity, no rotation; a leaked key affects
  every worker that shares it.
- Delegation requires carrying and storing the user's token. This is now **encrypted at rest**
  (AES-256-GCM under a server-held `GOWE_TOKEN_KEY`) in both `submissions.user_token` and the
  bearer credential embedded in `tasks.runtime_hints`; with no key the server fails closed on
  delegated submissions unless `--allow-plaintext-tokens` is set. The remaining shared-worker-key
  and transport items above are tracked as hardening work; see
  [`SPECIFICATION.md`](../../SPECIFICATION.md) §13.5–§13.7.

**Neutral**
- Anonymous mode exists as a convenience for local/dev use; it is off by default and executor-scoped.

## Alternatives considered

- **Run our own identity/password store** — full control, but duplicates the providers, adds a
  credential-management burden, and fragments where users' identities live. Rejected.
- **OAuth/OIDC against the providers** — cleaner token semantics and signature verification,
  but heavier to implement and not uniformly offered by the target services. Deferred as a
  possible future upgrade to token validation.
- **mTLS / per-worker certificates** — strong, rotatable per-worker identity, but requires PKI
  issuance and operations that outweigh the benefit at current scale. Noted as the hardening
  path if shared keys prove insufficient.

## References

- Code: `internal/server/auth.go`, `internal/server/worker_auth.go`, `internal/server/admin_config.go`, `pkg/model/user.go`, `internal/worker/worker.go`, `internal/executor/bvbrc.go`
- Docs: [`SPECIFICATION.md`](../../SPECIFICATION.md) §13
