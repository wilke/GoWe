# Authentication & Token Flow

**Scope:** How identities are established in GoWe, how BV-BRC provider tokens are verified, stored, passed to executors/apps/the BV-BRC API, and what isolates them.
**Verified against:** `main` @ v0.18.1 (`9db5981`).
**Audience:** operators and engineers. Companion doc: [`worker-isolation.md`](worker-isolation.md) (execution sandboxing), [`../PRODUCTION.md`](../PRODUCTION.md) (deployment flags).

---

## 1. Identities: who can talk to GoWe, and how

| Principal | Credential | Where checked |
|---|---|---|
| API user (BV-BRC) | `Authorization: Bearer <token>` (or `OAuth <token>`; bare token tolerated) | `internal/server/auth.go` `apiAuthMiddleware` |
| API user (MG-RAST) | `X-MG-RAST-Token` header | same middleware — **rejected by default when verification is on**; requires `--allow-unverified-mgrast` |
| Web UI user | username + password form → exchanged at `https://user.patricbrc.org/authenticate` for a BV-BRC token; a **session cookie** references it thereafter | `internal/ui/handlers.go` `HandleLoginPost` |
| CLI user | token resolved from `BVBRC_TOKEN` env → `~/.gowe/credentials.json` (`gowe login`) → `~/.bvbrc_token` / `~/.patric_token` / `~/.p3_token` | `internal/bvbrc/auth.go` `ResolveToken` |
| Anonymous | none — only if the server runs `--allow-anonymous`; executor use restricted by `--anonymous-executors`; optional rate limit | same middleware |
| Worker | `X-Worker-Key` header; keys from `--worker-keys` JSON file or admin API (DB-stored **hashed**) | `internal/server/worker_auth.go` |
| Admin | a normal user whose username is listed via `--admins` / `GOWE_ADMINS` / config file; role stamped on the user row | same middleware + `requireAdmin` |

Notes:

- The UI never accepts a pasted token: the password is sent only to BV-BRC's own auth endpoint, and the token GoWe holds comes from that response. GoWe never stores passwords.
- Session cookies are `HttpOnly`, `SameSite=Strict`, and `Secure` when TLS is configured (`--tls-cert/--tls-key`, `--secure-cookies`, or `--behind-proxy`).
- Every authenticated request lazily creates/updates a GoWe user row (`GetOrCreateUser`; concurrent-safe upsert since v0.18.1).

## 2. Token format and verification (since v0.18.1)

BV-BRC tokens are **self-contained** pipe-delimited bearer tokens:

```
un=<user>|tokenid=<uuid>|expiry=<unix>|client_id=…|token_type=Bearer|realm=…|SigningSubject=<url>|sig=<hex>
```

The signature is RSA PKCS#1 v1.5 over SHA-1, computed over everything **before** `|sig=`. Verification (`internal/bvbrc/verify.go`) is on by default and ordered deliberately:

1. **Parse** only the signed region; first occurrence of a field wins; anything after the signature hex is discarded.
2. **Pin the issuer**: `SigningSubject` must match a hard-coded allowlist of the four canonical BV-BRC key URLs (`user.patricbrc.org`, `user.bv-brc.org`, `user.alpha.…`, `user.beta.…` `/public_key`). This is checked **before any network I/O** — a client-supplied URL is never used to fetch a verifying key. The allowlist is compile-time; there is intentionally no flag to widen it.
3. **Expiry**: checked on every request; a token without an `expiry` field is rejected (never immortal).
4. **Signature**: verified against the pinned issuer's public key. Keys are cached 24 h; on a failed signature one rate-limited (60 s) refetch handles key rotation; if a refetch fails, a stale cached key is used with a warning.
5. Only after all of the above are `un=`/`tokenid=` trusted.

Failure semantics: invalid/expired/unpinned → **401**; key server unreachable with no cached key → **503** (`UNAVAILABLE`) so an issuer outage is distinguishable from bad credentials.

Escape hatches and controls:

- `--insecure-skip-token-verify` — disables verification (air-gapped/dev); identities are then trusted as claimed.
- `--auth-denylist <file>` — local, immediate revocation: `user:<name>` or `tokenid:<uuid>` per line; applies in both verified and skip-verify modes.
- `--allow-unverified-mgrast` — re-opens the MG-RAST header path, which has no signature scheme.

**Honest limits of the format** (inherited, not fixable in GoWe): tokens carry **no audience claim** — the same token is valid at GoWe, the BV-BRC Workspace, Shock, and any other consumer; and there is **no pre-expiry revocation** upstream — the local denylist is the only kill switch before `expiry`.

## 3. Token lifecycle inside the server

```
request (Bearer token)
  └─ verify (§2) → UserContext{user, token, expiry}
       └─ POST /submissions → Submission.UserToken   (json:"-" — never serialized to API clients)
            └─ persisted encrypted (AES-256-GCM)      submissions.user_token
                 └─ attached to a Task only when needed (§4) as
                    RuntimeHints.StagerOverrides.HTTPCredential   tasks.runtime_hints (also encrypted)
                      └─ scrubbed at terminal state (scrubTaskToken)
```

- **At rest**: `GOWE_TOKEN_KEY` / `--token-key-file` configures AES-256-GCM; each ciphertext is bound to its row and column via GCM AAD (`submission.user_token:<id>`, `task.runtime_hints.http_credential:<id>`), so a ciphertext cannot be relocated to another row. Policy is three-way: key set → encrypt; no key → **refuse to persist** tokens (fail closed) unless `--allow-plaintext-tokens` restores legacy plaintext with a warning. Pre-encryption plaintext rows are read transparently and can be upgraded (`ReencryptPlaintextTokens`).
- **In responses**: `Submission.UserToken` is tagged `json:"-"` — it never appears in the user-facing API or UI.
- **Scrubbing**: when a task reaches a terminal state the credential is removed from its persisted runtime hints — tokens live in task rows only while work is in flight.
- **Sub-workflows**: child submissions inherit the parent's `UserToken` (they run the same user's work); the sub-workflow **proxy task deliberately carries none** (it never executes — a token at rest there would be pure exposure).

## 4. When does a task carry the user's token?

`internal/scheduler/loop.go` `addUserToken` — the token is attached to a task **only** when:

1. the task targets the **bvbrc executor** (it cannot call the BV-BRC AppService without it), or
2. the tool opted in via the CWL hint `gowe:Execution.inject_bvbrc_token: true`, or
3. the task's worker group is listed in `--token-inject-groups` (group-level policy; also flips the same hint so the worker enforces it), or
4. **passthrough staging mode**: the server was started *without* `--workspace-staging server`. Then workers do their own `ws://` staging and **every task** carries the submitter's token.

Deployment consequence: in server-staging mode (recommended; production runs it) the default is **no token in the task** — staging happens server-side and the token stays in the server. In passthrough mode the isolation story is materially weaker; choose it knowingly.

## 5. How the token reaches the BV-BRC API

All BV-BRC-side calls authenticate with the **submitter's own token** — GoWe holds no service account and never mints credentials:

| Call | Where | Auth |
|---|---|---|
| `AppService.start_app` / `query_tasks` / kill | `internal/executor/bvbrc.go` (reads `taskToken()` from the task's runtime hints) | token in the JSON-RPC `Authorization` header |
| Workspace create/ls/get/upload/download (server-side pre/post-staging) | `internal/scheduler/workspace.go` → stager `.WithToken(sub.UserToken)` | `Authorization: OAuth <token>` |
| Shock uploads/downloads | `pkg/bvbrc/workspace.go` | `Authorization: OAuth <token>` |

Workspace paths are derived from the **verified** `un=` claim, used verbatim (`/<un>/home/...` — never suffixed with a domain).

## 6. How the token reaches apps/containers

Only via **explicit opt-in** (§4 conditions 2–3). The worker (`internal/worker/worker.go` `injectBVBRCTokenEnv`) injects the token into the tool's container environment as:

- `BVBRC_TOKEN`
- `KB_AUTH_TOKEN`

Nothing else is injected — a tool that does not opt in runs with **no ambient credential** (behavior restored in v0.18.0, #133/#229; a scan of 490 registered workflows found zero relying on the old ambient injection). Worker-local `--secret NAME=value` / `--secret-file` env vars are injected into every container by that worker but are **never sent to the server**, stored in task data, or exposed via the API.

## 7. Trust boundaries and isolation properties

- **Server ↔ API clients**: token verified per request; never echoed back (`json:"-"`); denylist applies post-identity.
- **Server ↔ workers**: the worker checkout payload **does** carry the plaintext credential inside `RuntimeHints.StagerOverrides.HTTPCredential` when §4 applies — that *is* the designed delivery mechanism. It is gated by `X-Worker-Key` auth; **transport confidentiality is the deployment's TLS story** (`--tls-cert/--tls-key` or a TLS-terminating proxy with `--behind-proxy`). On loopback-only deployments the wire is the local host.
- **Server ↔ DB**: encryption boundary at the store; in-memory values are plaintext, rows are AES-256-GCM.
- **Worker ↔ container**: secrets and the token reach the tool as environment variables. Since #260 they are handed to the container runtime through the worker's *process environment* (Docker `-e NAME` with the value in `cmd.Env`; Apptainer `APPTAINERENV_NAME`), never on the runtime's argument vector — so they are not visible in `ps`/`/proc/*/cmdline` on the host. Every log line that prints a tool or runtime command is masked (`toolexec.MaskSecretValues`); `--env-file` values are still logged in clear at worker start (put credentials in `--secret-file`, which logs names only).
- **Container ↔ host / other tasks**: out of scope here — see [`worker-isolation.md`](worker-isolation.md) (containers run as root, NetworkAccess is tool-declared, etc.).
- **UI sessions**: cookie references a server-side session that holds the token; the token itself is never sent to the browser.

## 8. Submission-time secrets (#260)

Separate from the submitter's BV-BRC/MG-RAST **provider token** (§§1–7): a submission may also carry arbitrary **application secrets** — API keys, DSNs, HuggingFace tokens — that a tool needs but that must never appear in the workflow record, task inputs, or logs. The lifecycle deliberately mirrors the token model above.

**What they are:**

- `secrets: {NAME: value, …}` on `POST /api/v1/submissions` — submitter-supplied, name must match `^[A-Z][A-Z0-9_]*$`, value 1–64KiB, at most 64 entries per submission.
- A top-level `cwltool:Secrets` hint (`hints: {"cwltool:Secrets": {secrets: [inputID, …]}}`, cwltool's own extension — never standardized, see the issue discussion) declares specific **workflow inputs** as secret. At submission the server moves each declared input's value out of `inputs`/`submitted_inputs` and into the secrets store, replacing it with the literal placeholder `<secret>`; the key it's stored under is derived deterministically as `INPUT_<INPUT_ID_UPPERCASED>` (`pkg/model.SecretNameForInput`) so the worker can re-inject it without any extra bookkeeping.

**Lifecycle (mirrors §3's token diagram):**

```
POST /submissions {secrets: {...}}
  └─ ValidateSecrets (name/size/count) → Submission.Secrets   (json:"-" — never serialized)
       └─ persisted encrypted (AES-256-GCM, same GOWE_TOKEN_KEY as §3)   submissions.secrets
            └─ per-task opted-in subset attached at dispatch (below) as
               RuntimeHints.Secrets   tasks.runtime_hints (also encrypted, under "__enc__")
                 └─ scrubbed at terminal state (scrubTaskToken)
                      └─ submission-level value purged per retention policy (below)
```

**Delivery opt-ins** — a task sees a submission secret only if its step explicitly asks:

| CWL hint | Effect |
|---|---|
| `gowe:Execution.secret_env: [NAME, ...]` | that task's container gets exactly those env vars; an unlisted or absent name fails the task **pre-dispatch**, naming the missing secret, never dispatching it |
| `gowe:Execution.inject_secrets: true` | every submission secret is injected as an env var (used for tools that legitimately need the whole set, e.g. a registry credential bundle) |
| `cwltool:Secrets` (top-level) | the declared input's value is re-injected into the in-memory job before tool evaluation (parameter references, `InitialWorkDirRequirement` interpolation), never into the persisted job |
| (no hint) | the task sees no submission secrets at all |

A step with no opt-in sees nothing; sibling steps in the same submission are isolated from each other's secrets by default — this is what makes a shared worker group safe for multi-tenant secrets. A sub-workflow's child submission **inherits** the parent's secrets store (it's the same submitter's work); the sub-workflow **proxy task carries none** — it never executes, so a value at rest there would be pure exposure (same reasoning as the token's proxy handling, §3).

**Retention policies** — a submission's secret *values* are purged automatically once terminal, per policy (`secret_names` and other metadata are always kept for auditability):

| `secrets_retention` | Behavior |
|---|---|
| `keep` | never purged automatically (today's token default; used by dev/demo tenants for reproducible debugging) |
| `ttl:<duration>` | purged that long after the submission reaches a terminal state — **server default: `ttl:720h`** (30 days), set via `--secrets-retention` |
| `on_success` | purged on COMPLETED; kept on FAILED/CANCELLED so a retry still has them |
| `on_terminal` | purged as soon as the submission reaches any terminal state |
| manual | `DELETE /api/v1/submissions/{id}/secrets` (owner/admin), any time; cascades to descendant child submissions |

A per-submission `secrets_retention` in the create request overrides the server default. The scheduler's retention sweep (`internal/scheduler/secrets_retention.go`) evaluates every terminal submission still carrying a value once a tick, rate-limited to once per minute. `POST .../retry` on a submission whose secrets were purged is refused with **409** (`"secrets purged; resubmit with secrets"`) rather than silently retrying without them.

**Surfaces that never show a value** — create response, `GET /submissions/{id}`, list, and the web UI's submission detail page all expose only `secret_names`, `secrets_state` (`none|present|purged`), `secrets_retention`, and `secrets_purged_at`; the value itself is `json:"-"` at every layer and is never present in these responses.

**`cwltool:Secrets` compatibility** — cwltool's own extension (never standardized into the CWL spec; a CWL v1.3 draft, PR #26, proposes a different `SecretText` construct instead) is supported for both the server path (stripped at submission as above) and the standalone `cwl-runner` path. `cwl-runner` has **no submission store** — there is nothing to strip, since the value comes straight from the job file — so its only obligation is that its **own logs/provenance never carry the value**; there is no metadata/placeholder round-trip to speak of outside a server-managed submission.

**Honest limits:**

- The **worker sees secret values in flight**: they arrive in the checkout payload (`RuntimeHints.Secrets`) exactly like the provider token (§7) — the same TLS/loopback trust boundary applies.
- Once delivered, the value is an **ordinary environment variable inside the tool's own process** — any code the tool runs can read it, log it, or write it to a file; GoWe's guarantees stop at "GoWe itself never persists or displays it," not "the tool can't leak it."
- **Argv/log masking is best-effort string replacement** (`redactSecrets`/`maskSecretValues`, values ≥8 bytes — enforced by validation), applied to the worker's captured stdout/stderr and to the container-runtime argv debug log. It cannot catch a value the tool has transformed (base64'd, split across lines, etc.) before printing it.
- **Known limitations of this release** (tracked on issue #260; fixed in a follow-up before general availability — see the PR/branch history for `feat/260-fixes`):
  - `cwltool:Secrets` re-injection only recognizes a **direct** `in: x: <workflow-input>` step sourcing at the **top-level workflow**; sourcing through an intermediate step output, `valueFrom`, or a nested sub-workflow's own `cwltool:Secrets` declaration is not (yet) re-injected.
  - A task's output *files* are never scanned or redacted — if a tool writes a secret value into a declared output file (by design, e.g. a rendered config), that file is not treated specially; only the task's captured stdout/stderr get the `***REDACTED***` treatment.
  - Files materialized via `InitialWorkDirRequirement` interpolation (the IWDR consumption mode) are created with the worker's default file modes — no extra permission hardening beyond normal task-directory isolation.
  - Task API surfaces (`GET .../tasks/{tid}`, `GET .../tasks/`, embedded `tasks[]`, admin active tasks, SSE) sanitize `RuntimeHints.Secrets` exactly as they sanitize the provider credential; worker-executed tasks are scrubbed at terminal state, on cancel, and on purge. The acceptance battery (`internal/server/e2e_secrets_test.go`, `scripts/validate-secrets.sh`) asserts each of these.

## 9. Operator quick reference

| Flag / env | Purpose |
|---|---|
| `--insecure-skip-token-verify` | disable signature verification (dev/air-gapped only) |
| `--auth-denylist <file>` | local user/token revocation (`user:`/`tokenid:` lines) |
| `--allow-unverified-mgrast` | re-enable the unverified MG-RAST header path |
| `GOWE_TOKEN_KEY` / `--token-key-file` | AES-256-GCM key for tokens at rest (32 bytes, base64/hex) |
| `--allow-plaintext-tokens` | permit plaintext at rest when no key is set (migration/dev) |
| `--token-inject-groups g1,g2` | group-level token injection policy (§4.3) |
| `--workspace-staging server` | keep tokens server-side for `ws://` staging (§4.4) |
| `--worker-keys <file>` | static worker keys (`X-Worker-Key`) |
| `--admins` / `GOWE_ADMINS` | admin usernames |
| `--allow-anonymous`, `--anonymous-executors` | anonymous access policy |
| `--tls-cert/--tls-key`, `--behind-proxy`, `--secure-cookies` | transport & cookie security |
