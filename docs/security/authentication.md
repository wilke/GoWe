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
| Service (a program, e.g. the model router) | `Authorization: Bearer gowe_svc_…`, a token GoWe issues (`gowe admin service-tokens create`); identity `<name>@service.gowe`, role user, never admin, never a group manager, no provider token, no BV-BRC executor. CLI: `gowe --token-file FILE` | `internal/server/auth.go` `authenticateServiceToken`; see [Service tokens](#service-tokens) |
| Anonymous | none (no `Authorization` / `X-MG-RAST-Token` header; an empty one is `401`) — only if the server runs `--allow-anonymous`; executor use restricted by `--anonymous-executors`, never the BV-BRC executor (it needs the submitter's token: `403`); optional rate limit | same middleware |
| Worker | `X-Worker-Key` header; keys from `--worker-keys` JSON file, the admin API, or a group manager (DB-stored **hashed**) | `internal/server/worker_auth.go` |
| Admin | a normal user whose username is listed via `--admins` / `GOWE_ADMINS` / config file; role stamped on the user row | same middleware + `requireAdmin` |
| Worker group manager | a normal user named in a group's `managers` by an admin (never a service identity); identified by username only | same middleware + `internal/server/handler_worker_group_managers.go` |

Notes:

- The UI never accepts a pasted token: the password is sent only to BV-BRC's own auth endpoint, and the token GoWe holds comes from that response. GoWe never stores passwords.
- Session cookies are `HttpOnly`, `SameSite=Strict`, and `Secure` when TLS is configured (`--tls-cert/--tls-key`, `--secure-cookies`, or `--behind-proxy`).
- Every authenticated request lazily creates/updates a GoWe user row (`GetOrCreateUser`; concurrent-safe upsert since v0.18.1).
- Worker keys authorize **workers**: which groups a worker may join. They say nothing about who may send work to a group. Which **submitters** may target a worker group (CWL hint `gowe:Execution.worker_group` or the `worker_group` label) is authorized by the worker group access policy (`internal/groupacl`, #261), stored in the database (`worker_group_policies`, and `worker_groups.default_deny` in `server_settings`) and changed at runtime by server admins (`gowe admin worker-groups`, `/api/v1/admin/worker-groups`): a group with a rule is usable only by its `users` and its `managers` (and server admins when `admins` is true). A change takes effect at once: it is written to the database and loaded into the policy the server enforces. The policy is checked when a task is created or re-queued: the submission handlers refuse a denied group with 403, the scheduler re-checks at dispatch before any worker task is created and before it re-queues a failed worker task for retry, and the UI's resume/recompute actions refuse to re-queue a denied task. Tasks already QUEUED or RUNNING when the policy becomes stricter are not re-checked; to revoke access for work already queued, cancel the user's submissions. The server loads the policy at start-up and refuses to start when a stored rule is invalid; a direct SQL edit of the tables is only seen after a restart. Anonymous submitters (stored as `anonymous`) are never listed and never admins. The policy is only as strong as the username: with `--insecure-skip-token-verify` or `--allow-unverified-mgrast` a caller can claim a listed username (the server logs a WARN at start-up when the policy then loaded has restrictions). See [server.md, Worker Group Access](../tools/server.md#worker-group-access).
- Worker authentication does not depend on the number of key rows. It is on while static keys are configured or the server setting `worker_auth.required` is set (any value other than `false`, case-insensitive; a missing row or `false` is off); creating a database worker key sets it to `true` (same transaction), and at start-up a database that holds keys gets it. **Once a key has existed, worker authentication stays on**, also after the last key is revoked. To go back to an open fleet, an operator (with no static keys configured) deletes **all** rows of `worker_keys` and the `worker_auth.required` row of `server_settings`; deleting only the setting is not enough, since the server sets it again at start-up while key rows exist. No restart is needed: the setting is read on every worker request.
- **Worker group managers** (`managers` of a rule, appointed by server admins) manage, for their group only, its `users` and the worker keys valid for exactly that group (`/api/v1/worker-groups`, `gowe worker-groups`); managers, `admins`, `default_deny` and deleting a rule stay admin-only, and the default group cannot have managers. Trust statement:
  - Whoever can issue a key for a group can run a worker that receives **every task sent to that group**, including the submitter's data and the token used for staging (§4, §7). Appointing a manager delegates that much.
  - Revoking a key does not stop a task that is already running on a worker, nor recall the token it received.
  - Removing a manager does not revoke the keys they created: they may hold the raw keys. The admin's answer lists the group's keys (`group_key_ids`); revoking them is the admin's decision. An admin changing the groups of a key a non-admin created gets a WARN in the server log.
  - Static keys (`--worker-keys`, `GOWE_WORKER_KEYS`) are outside manager control.
  - Managers are identified by username, like the policy: with `--insecure-skip-token-verify` or `--allow-unverified-mgrast` a caller can claim a manager's name (the start-up WARN says so).

### Service tokens

Every other API identity is a BV-BRC (or MG-RAST) user. A program that submits work on its own (the model router) gets a **service token** that GoWe itself issues, so the worker group policy can name it.

- **Format**: `gowe_svc_` followed by 43 characters of unpadded base64url of 32 bytes from `crypto/rand` (52 characters in all).
- **Storage**: only `sha256(token)` (lower-case hex) and the first 16 characters (`token_prefix`, for display) are stored, in the table `service_tokens`. The raw token is returned once, by the create call (`POST /api/v1/admin/service-tokens`, `Cache-Control: no-store`); `gowe admin service-tokens create` writes it only to its `--out` file (mode 0600, never overwritten) and never prints or logs it. The server never logs the token or its hash (it logs the token id and name: DEBUG on use, INFO on create and revoke with the acting admin).
- **Identity and realm**: a request authenticated with a token of name `N` is the user `N@service.gowe`, provider `service`, role user. The realm `service.gowe` is **reserved**: a BV-BRC or MG-RAST token whose username ends in `@service.gowe` (case-insensitive) is refused with `401`, verified or not, and the web UI login refuses such a username before contacting BV-BRC. The realm can only be reached with a service token.
- **Never admin**: a username in the service realm is never an admin, even when listed in `--admins` / `GOWE_ADMINS` / the config file or stored with role admin: `model.User.IsAdmin` and `AdminConfig.IsAdmin` both refuse the realm, the middleware gives the request role user, `requireAdmin` refuses a service identity, and `PUT /api/v1/admin/users/{username}/role` refuses to make one admin. A group rule with `admins: true` does not admit it.
- **No provider token**: the request carries no BV-BRC token, exactly like an anonymous submission. Its submissions are stored with `submitted_by` = the service username and no token, so there is no Workspace staging (`ws://` inputs and an `output_destination` need the submitter's token: the server skips pre-staging and fails the output upload, a worker gets no staging credential) and no token injection (a tool requesting the token is refused at submit and at dispatch).
- **Never the BV-BRC executor**: a service identity cannot use the BV-BRC executor, and the server's own BV-BRC account is never used for it. `POST /api/v1/submissions` (also with `dry_run`) and `PUT /api/v1/submissions/{id}/retry` answer `403 service identities cannot use the BV-BRC executor` when any top-level step resolves to `bvbrc` as known at submission time (`--force-executor`, the step's `gowe:Execution.executor`, `--default-executor`); a retry is refused when the submitter or the caller is a service identity. The scheduler is authoritative: it fails such a step at dispatch with the same reason before any task exists (nested sub-workflows and scatter steps included), and refuses to re-queue a BV-BRC task of a service submission (scheduler retry, UI resume/recompute). The same holds for every submission without the submitter's provider token, anonymous ones included whatever `--anonymous-executors` says: `POST /api/v1/submissions` (also with `dry_run`) and `PUT /api/v1/submissions/{id}/retry` answer `403 the BV-BRC executor needs the submitter's BV-BRC token; this submission carries none (anonymous or service identity)` (a service identity gets the reason above first; a retry is refused when the caller or the submission carries no token), and the scheduler fails such a task once, permanently (no retries), before dispatch with the same reason. Last, the BV-BRC executor starts a job only with the task's own token: a task without one fails, permanently, with `no user token for BV-BRC submission`, whoever submitted it. The server's BV-BRC account (`bvbrc service account ready` at start-up) answers `/apps` and polls, cancels and reads logs of existing jobs whose task carries no token; it never submits one.
- **Handling**: any credential that contains `gowe_svc_` is handled only as a service token: it never reaches the BV-BRC verifier or the unverified parser. A malformed, unknown, expired or revoked token gets the same `401 invalid token format` as any other bad token; a database error gets `503`. A service token in the `X-MG-RAST-Token` header is refused. Header forms: `Authorization: Bearer <token>` (the `Bearer ` prefix is matched case-sensitively; white space around the token and a bare token are tolerated); `bearer <token>` or any other scheme is taken verbatim as the credential, which is then not a well-formed service token: `401`, never the anonymous path. A request with more than one `Authorization` (or more than one `X-MG-RAST-Token`) header is refused with `401`, for every kind of credential, and so is a request whose `Authorization` or `X-MG-RAST-Token` header is empty after the `Bearer` scheme (`""`, `Bearer`, white space): it is never served as anonymous. A request with neither header is anonymous when `--allow-anonymous` is on. Service tokens work with and without `--insecure-skip-token-verify` and whatever `--allow-anonymous` says; the `--auth-denylist` applies to the service username (`user:model-router@service.gowe`). `last_used_at` is written at most once a minute.
- **Expiry**: optional, `expires_in` between `1h` and `8760h` at creation; without it the token does not expire (stored `expires_at` NULL). An expired token is refused at once. A stored `expires_at` that is empty or cannot be parsed (a hand-edited row) never means "no expiry": the token counts as expired (`401`, an ERROR in the server log with the token id; the admin list shows it expired, with the zero time).
- **Revocation**: `gowe admin service-tokens revoke <id>` (`DELETE /api/v1/admin/service-tokens/{id}`) deletes the row; the next request is refused. Work already submitted is not cancelled. Rotation: create a second token for the same name, switch the program, revoke the old one (at most 20 tokens per name, 500 in total). Revoking every token of a name does **not** remove the identity: its user row, the worker group rules that name it and its submissions stay, and a new token created later with the same name is the same user and inherits all of them. To retire an identity, also remove its name from the worker group rules (and cancel its work).
- **Allowing one in a worker group**: `gowe admin worker-groups set model-router --user model-router@service.gowe`. A service identity can be in a rule's `users`, never in its `managers`: the admin API refuses it (`400` naming the entry), and a stored rule that names one anyway (written by SQL or before this release) is loaded with that entry ignored and one WARN per reload; the rule's other fields apply and start-up does not fail. The manager routes (`/api/v1/worker-groups`) give a service identity no manager right.
- **Rollback**: before going back to an older server binary, remove every `…@service.gowe` name from the worker group rules (`users` and `managers`). An older server does not reserve the realm: with `--insecure-skip-token-verify` or `--allow-unverified-mgrast` anyone could claim such a name and with it the group access (and the identity's submissions).
- **Realm rows at start-up**: the server logs one WARN naming the user rows whose username is in the realm but whose provider is not `service` (rows made before the realm was reserved, or by hand). A service token of such a name acts as that user (its submissions, workflows, rules), never as an admin; it does not block start-up.
- **Trust statement**: whoever holds the token *is* the user `<name>@service.gowe` and can do exactly what follows (checked against the code of this release):
  - **Run work**: submit to every open worker group (including the default group) and to restricted groups that list the username in `users` (a rule with `admins: true` does not admit it). It is **not** bound by `--anonymous-executors`: it can use every executor the server offers to normal users except `bvbrc`. That includes the `local` executor, which runs the tool on the **server host**: a step selects it with `gowe:Execution.executor: local`, and a step with no executor hint (and no DockerRequirement while workers are online) gets it when `--default-executor` is unset or `local`; only `--force-executor` takes the choice away. Its submissions carry no BV-BRC token (no Workspace staging, no token injection).
  - **Secrets**: attach submission secrets (`secrets`, `cwltool:Secrets` inputs) to its own submissions, choose their retention and purge them.
  - **Workflows**: register workflows; update, relabel or delete its own and those with an empty owner (`created_by` empty: registered before ownership was recorded); list every workflow (summaries) and read any workflow in full, CWL included (`GET /api/v1/workflows/{id}`, `/inputs`, `/outputs`), like every authenticated user, and submit any of them.
  - **Files** (when the file proxy is on, `--upload-backend`): upload with `POST /api/v1/files` using the server-held upload credentials (Shock token, S3 keys or the local upload directory), and read any file or directory listing under the allowed download directories (`--upload-download-dirs`, default the local upload directory) with `GET /api/v1/files/download`, with no owner check, other users' uploads included.
  - **Read-only views**: `/api/v1/apps` (answered with the server's own BV-BRC account, cached), `/api/v1/fleet` (every worker's id, name, group, runtime, version, state, last seen, current task id and degraded-mode labels; a worker's name defaults to its machine's hostname), `/api/v1/hosts` (per-host capacity, claims, use and GPUs; the `hostnames` field and hostname-bearing legacy host ids are redacted, worker names are not), `/api/v1/labels`, and `/api/v1/workspace` (a static placeholder).
  - It **cannot**: read, list, stream (SSE), cancel, retry or delete other users' submissions, or read or purge their secrets (`403`, as for any other non-owner); see workers' peer addresses (admins and the group's managers only) or the full worker records (`/api/v1/workers` needs a worker key); be an admin (whatever `--admins`, `GOWE_ADMINS`, the config file or a stored role say); be a worker group manager; use the web UI (its login refuses the realm); use the BV-BRC executor.
  - **Tasks of other submissions**: the task endpoints (`GET /api/v1/submissions/{id}/tasks/`, `.../tasks/{tid}`, `.../tasks/{tid}/logs`, `.../tasks/{tid}/attempts`) and the web UI's task pages check the submission named in the path: an unknown submission is `404`, another user's `403`, and a task that does not belong to it `404`. Knowing a task id (`/api/v1/fleet` shows the id of each worker's current task) reads nothing.
  - **Recommendation**: issue one token per program; name it only in the `users` of the groups it needs (with `default_deny` on, every other group except the default group is closed to it); set an expiry (`--expires`) wherever the program can be re-keyed; revoke at once on suspicion (and cancel its work, which revocation does not stop). Keep the token file on the program's host and use `gowe --token-file FILE`: a regular file (never a symbolic link), owned by the user running `gowe`, mode 0600 or stricter, at most 4096 bytes. There is no environment variable or argument form for it.

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

`internal/scheduler/credentials.go` `attachCredentials` (the single entry point: `applyTokenGrant` -> `addUserToken` -> `addSecrets`, used by dispatch, scatter dispatch and retries) embeds the token as the task's HTTP credential **only** when:

1. the task targets the **bvbrc executor** (it cannot call the BV-BRC AppService without it; header only, unchanged), or
2. the tool **requests** the token (`gowe:Execution.inject_bvbrc_token: true` in the tool document's `hints`) **and** an administrator **blessed that exact tool** (see `docs/security/tool-blessing.md`) **and** the executor is `worker` and the worker group is allowed: the grant also sets `InjectBVBRCToken` + the blessing metadata, or
3. a **worker** task needs it for **staging**: `ws://`/`shock://` inputs in its job, or the submission has an `output_destination`. This is need-based and identical in passthrough and server-staging mode; it never causes injection into the tool environment, or
4. (deprecated, staging-only) the worker group is listed in `--token-inject-groups`.

There is no longer a "passthrough mode puts the token in every task" clause. A step cannot request the token; unblessed requests are refused (`400 TOOL_NOT_BLESSED` at submit, task failed pre-dispatch otherwise). `--token-blessing=dev` grants unblessed requests too (loud; development only).

Who uploads what: the worker uploads its own outputs to the output destination whenever it holds the credential; in server-staging mode the server pre-stages `ws://` inputs and its post-stage uploads only leftover `file://` outputs plus `_gowe_outputs.json`.

## 5. How the token reaches the BV-BRC API

All BV-BRC-side calls authenticate with the **submitter's own token** — GoWe holds no service account and never mints credentials:

| Call | Where | Auth |
|---|---|---|
| `AppService.start_app` / `query_tasks` / kill | `internal/executor/bvbrc.go` (reads `taskToken()` from the task's runtime hints) | token in the JSON-RPC `Authorization` header |
| Workspace create/ls/get/upload/download (server-side pre/post-staging) | `internal/scheduler/workspace.go` → stager `.WithToken(sub.UserToken)` | `Authorization: OAuth <token>` |
| Shock uploads/downloads | `pkg/bvbrc/workspace.go` | `Authorization: OAuth <token>` |

Workspace paths are derived from the **verified** `un=` claim, used verbatim (`/<un>/home/...` — never suffixed with a domain).

## 6. How the token reaches apps/containers

The worker holds the submitter's token **only to stage data in and out**, and drops it when the task ends (`scrubTaskCredentials` in `internal/worker/tokenpolicy.go`, deferred in `executeTask`, so it runs on success, failure, cancellation and panic; Go strings cannot be zeroed, so this releases the last references rather than overwriting memory).

It places the token in a tool's environment (`injectBVBRCTokenEnv`) only when **all** of these hold:

1. the scheduler set `RuntimeHints.InjectBVBRCToken` **and** a `TokenBlessingID` (the tool is blessed, see [`tool-blessing.md`](tool-blessing.md)); the worker never reads the tool's own hints for this decision. A request without a blessing id (old scheduler, `--token-inject-groups`) is refused: the worker logs `token injection requested without blessing; refusing` at WARN and runs the task **without** the token;
2. a credential is present in `StagerOverrides.HTTPCredential`;
3. the **image digest check** passes. Before launch, the worker resolves the image exactly as execution will (`toolexec.ResolveApptainerImage` for `.sif`, then `filepath.EvalSymlinks`), streams a sha256 of that file and requires it to be in `RuntimeHints.ImageSHA256s` (the blessing's digest list, case-insensitive). The very path that was hashed is what gets launched. Registry references (Docker, or non-`.sif` Apptainer) must carry `@sha256:<digest>` with a blessed digest. Local (non-container) execution can never carry the token. Any mismatch, an empty digest list or an unpinned ref **fails the task before launch** with an error naming the resolved path, its digest and the expected digests (never the token).

The names used are `RuntimeHints.TokenEnvNames`, or `BVBRC_TOKEN` and `KB_AUTH_TOKEN` when empty. The token is always in the redaction mask for the reported stdout/stderr and a scan replaces it with `***REDACTED***` in reported outputs (WARN with the task id), even for staging-only tasks. Files a tool wrote to disk are not rewritten. Hashing only happens for tasks that will receive the token.

Nothing else is injected — a tool that is not blessed runs with **no ambient credential** (behavior restored in v0.18.0, #133/#229; a scan of 490 registered workflows found zero relying on the old ambient injection). Worker-local `--secret NAME=value` / `--secret-file` env vars are injected into every container by that worker but are **never sent to the server**, stored in task data, or exposed via the API.

## 7. Trust boundaries and isolation properties

- **Server ↔ API clients**: token verified per request; never echoed back (`json:"-"`); denylist applies post-identity.
- **Server ↔ workers**: the worker checkout payload **does** carry the plaintext credential inside `RuntimeHints.StagerOverrides.HTTPCredential` when §4 applies — that *is* the designed delivery mechanism. It is gated by `X-Worker-Key` auth, and every `/workers/{id}/*` route additionally requires that the calling key be the one that registered that worker id (`workers.key_id`; `403` otherwise; legacy unbound workers are adopted by the first key allowed to join their group; checkout re-checks group scope). Workers sharing one key share one identity, so binding only separates distinct keys — per-group keys are needed for isolation between hosts. **Transport confidentiality is the deployment's TLS story** (`--tls-cert/--tls-key` or a TLS-terminating proxy with `--behind-proxy`). On loopback-only deployments the wire is the local host.
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
| `--token-inject-groups g1,g2` | **deprecated, staging-only**: staging credential for these groups; never injects (§4.4) |
| `--token-blessing enforce\|dev` | provider-token policy; `dev` also grants unblessed tools (loud, development only) |
| `--dev-image-dir <dir>` | dev mode: resolve relative `.sif` refs to pin the image digest |
| `--workspace-staging server` | pre/post-stage `ws://` on the server (§4); a worker still gets a staging credential when it uploads to an output destination |
| `--worker-keys <file>` | static worker keys (`X-Worker-Key`) |
| `gowe admin worker-groups` (stored in the database) | which submitters may use which worker groups, and who manages them (§1) |
| `gowe worker-groups` | group managers: users and group-only worker keys of their groups (§1) |
| `gowe admin service-tokens` | identities GoWe issues to programs (`<name>@service.gowe`, never admin; §1 Service tokens) |
| `server_settings` row `worker_auth.required` | keeps worker auth on once a DB key has existed (any value but `false`); to reopen the fleet delete all `worker_keys` rows and this row, no restart needed (§1) |
| `--admins` / `GOWE_ADMINS` | admin usernames |
| `--allow-anonymous`, `--anonymous-executors` | anonymous access policy |
| `--tls-cert/--tls-key`, `--behind-proxy`, `--secure-cookies` | transport & cookie security |
