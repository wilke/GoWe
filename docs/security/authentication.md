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
- **Worker ↔ container**: token/secrets enter as environment variables on the container runtime's argv (`--env NAME=value` for Apptainer, `-e` for Docker). **Caveat (live):** the full argument vector is logged at **debug** level (`internal/toolexec/execute.go`), so a worker running `--log-level debug` can write secret and token values to its log. Production runs `info`. This is finding #1 of [`worker-isolation.md`](worker-isolation.md) and is not yet fixed.
- **Container ↔ host / other tasks**: out of scope here — see [`worker-isolation.md`](worker-isolation.md) (containers run as root, NetworkAccess is tool-declared, etc.).
- **UI sessions**: cookie references a server-side session that holds the token; the token itself is never sent to the browser.

## 8. Operator quick reference

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
