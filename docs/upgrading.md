# Upgrading GoWe

Version-specific operator guidance. Routine upgrades (stop server, replace binaries,
restart) need no special steps — schema migrations run automatically at startup. Entries
below cover the exceptions.

## 0.15.x → next

Dispatch/staging attribution (#184 PR2) adds `tasks.dispatched_at`, `tasks.stage_in_ms`,
`tasks.stage_out_ms`, and four `submissions` staging timestamps
(`prestage_started_at`/`prestage_completed_at`/`poststage_started_at`/`poststage_completed_at`).
All eight columns are nullable and added via the normal idempotent `ALTER TABLE ADD COLUMN`
migration path — no manual step is required. Two behavior changes an operator should know
about:

- **`started_at` is now `NULL` on `QUEUED` worker and bvbrc tasks in the API/UI.**
  `submitAndUpdateTask` no longer stamps `started_at` for those two executors at dispatch
  time (`CheckoutTask` and, for bvbrc, `MarkTaskRunning` on first observed platform
  `RUNNING` are the sole writers now — see `internal/scheduler/loop.go`). Previously a
  `QUEUED` worker/bvbrc task carried a stale dispatch-time `started_at` that looked like a
  live duration but was not one; this is a deliberate fix, not a regression. Any script that
  read `started_at` off a `QUEUED` task row will now see `null` instead of that stale
  timestamp. The submission `/timing` endpoint's trust rules (added in the prior release)
  already treated a `QUEUED` task's `started_at` as untrustworthy, so its output is
  unaffected; the UI's "Duration" column on a `QUEUED` row now correctly shows `-` instead
  of a ticking (and misleading) live value.
- **Migration window:** a task that is `QUEUED` at the moment of the upgrade keeps whatever
  `started_at` value the old server wrote until it next reaches `RUNNING` or a terminal
  state — the migration only adds columns, it does not rewrite existing rows. The `/timing`
  trust rule (never trust a `QUEUED` row's `started_at`) is exactly the guard that makes
  this safe to ignore: both the pre-upgrade stale value and the post-upgrade `NULL` are
  already excluded from every duration computed there.

Worker/server version skew is safe in both directions: an old worker's `/complete` report
carries no `stage_in_ms`/`stage_out_ms` fields, so a new server simply persists `NULL` for
those columns (same as "staging did not occur"); a new worker talking to an old server has
those fields silently ignored by the old handler's decode struct. No coordinated rollout is
required — workers and servers can be upgraded independently and in either order.

The `GET /api/v1/submissions/{id}/timing` response (and `gowe status --timing`) gains
`dispatch_s`, `checkout_wait_s`, `stage_in_s`, `compute_s`, and `stage_out_s` per task, and
`prestage_s`/`poststage_s` on the submission — see [`API_GUIDE.md`](API_GUIDE.md) §7. The
CLI `--timing` table grows the corresponding columns; the web UI is unchanged aside from the
`started_at`-nulling behavior above.

Also new and entirely optional: Prometheus metrics on a second listener, off by default —
`--metrics-addr`, `--metrics-workflow-label`, `--metrics-label-cap` (see
[`docs/tools/server.md`](tools/server.md#metrics) and
[`PRODUCTION.md`](../PRODUCTION.md#metrics)).

## 0.14.x → 0.15.0

0.15.0 ([CHANGELOG](../CHANGELOG.md)) changes four things an operator has to know about:
scatter over sub-workflows becomes non-blocking (#164), worker task directories are cleaned
up after success (#157, shipped in 0.14.0 and documented here because most deployments
skipped that release), Workspace stage-out goes through verified Shock uploads (#171, #172,
#175), and an admin recovery path exists for outputs delivered before the fix (#180).

### Scatter over sub-workflows becomes non-blocking

0.15.0 replaces the inline serial sub-workflow executor with persisted proxy tasks paired
1:1 with child submissions
([ADR-0011](adr/0011-scatter-subworkflow-proxy-tasks.md), issue #164).

**Before upgrading: cancel any in-flight scatter-over-subworkflow submissions.**

Why: the pre-0.15 code never persisted the parent step's `DISPATCHED` state, so after the
upgrade the new scheduler sees the step as `READY` and re-dispatches the whole scatter
from scratch — creating a duplicate set of children — while the old-style children already
in the database become orphans that run to completion with their results discarded.
Submissions without sub-workflow steps are unaffected.

If orphaned children are already present after an upgrade (old rows whose
`parent_task_id` points at a task that was never persisted), either let them finish (their
results are discarded) or clean them up:

```sql
UPDATE submissions SET state='CANCELLED'
WHERE state IN ('PENDING','RUNNING')
  AND parent_task_id != ''
  AND parent_task_id NOT IN (SELECT id FROM tasks);
```

Also note two API behavior changes in 0.15.0:

- `GET /api/v1/submissions` excludes child submissions by default; pass
  `include_children=true` to list them (see [`API_GUIDE.md`](API_GUIDE.md) §7).
- `DELETE /api/v1/submissions/{id}` returns `409 Conflict` while any descendant child
  submission is active — cancel first and let the cascade finish.

### Worker task-directory cleanup (0.14.0, #157 / PR #158)

Workers now delete `<workdir>/<task-id>` and its `_tmp` and `_staging` siblings as soon as
the server has accepted the task's **SUCCESS** result. Before 0.14.0 every task's staged
inputs, scratch space, and output originals stayed on disk forever (production workers had
accumulated 16–18 GB each).

Directories are **retained** when:

- the task **FAILED** or was cancelled (SKIPPED) — these never reach the cleanup path;
- the submission was made with `gowe submit --debug` (persisted as the submission label
  `debug=true` and propagated to every task as `RuntimeHints.Debug`) — per-job retention
  for troubleshooting;
- the worker runs with `--keep-task-dirs` — keeps everything that lands on that worker;
- any reported output location still resolves into the task directory — the guard for
  in-place stage-out (`--stage-out local`, or a shared-filesystem stager without a
  stage-out directory) and for a failed `StageOut` that fell back to the local path. In
  those configurations the task directory *is* the store downstream tasks read from.

With a copying or uploading stage-out (`file:///shared`, `shock://`, `s3://`, `ws://`) the
staged copy is the inter-task hand-off, so deletion right after the report is safe.

What is **not** cleaned: the staged intermediates themselves. Files copied into the shared
stage-out directory (`--stage-out file:///...`) or uploaded to Shock stay after the
submission completes; submission-level retention is tracked in #159. If you relied on
inspecting a successful task's working directory after the fact, use `gowe submit --debug`
for that job or `--keep-task-dirs` on the worker.

### Workspace stage-out through verified Shock uploads (#171, #172, #175)

Before 0.15.0 the scheduler's `ws://` post-staging (`--workspace-staging server`) and the
CLI's Workspace input upload (`gowe submit --workspace-upload`) put the file bytes into the
inline `Content` field of `Workspace.create`. That field is a JSON string, and
`encoding/json` replaces every byte that is not valid UTF-8 with U+FFFD (three bytes for
one), so **every binary output delivered to a Workspace before 0.15.0 is corrupt in the
stored copy** (gzip, float arrays, PDBs with non-ASCII bytes, ...) while text outputs
round-tripped intact. The web UI's workspace upload had its own defects, fixed at the same
time: its small-file branch handed raw bytes to `Workspace.create`, which base64-encoded
them into the object, and its large-file branch read the Shock URL from the wrong
`ObjectMeta` slot. The engine recorded the pre-upload size and reported
`output_state=delivered`, so nothing looked wrong until a downstream sha256 manifest
failed (#172). The original bytes are not present in the stored copies; recovery needs
the local originals (see the next subsection). The entangled #171 bug — the engine parsed
the Workspace `ObjectMeta` tuple with the wrong slot layout (Shock URL is slot 11, not 10)
— was fixed with it.

0.15.0 uses the Workspace service's own upload protocol for **every** file, with
verification (normative description in [`SPECIFICATION.md`](../SPECIFICATION.md) §10.6):
`Workspace.create` with `createUploadNodes` → multipart `PUT` of the bytes to the returned
Shock node with an exact `Content-Length` → check of the Shock reply (size, md5 when
present) → `Workspace.update_auto_meta`, whose reply must echo the size. A failed upload
deletes its own placeholder object; there is no size below which bytes go back through
the JSON field. Only zero-byte files are stored inline.

What changes for operators:

- **Server `--workspace-url`** now configures both the scheduler's stager and the web UI's
  workspace browser/uploads (default: the production BV-BRC Workspace).
- **The web UI acts as the logged-in user only.** The server's service-account
  (`BVBRC_TOKEN`) path for user workspace operations is gone; every browse, list, and
  upload uses the session token. A session without a token gets `401` on the UI API and a
  redirect to `/login` on pages.
- **`--upload-max-size`** (default 1 GB) now also caps web UI workspace uploads (`413`
  when exceeded). Uploads larger than 32 MiB spill to disk instead of RAM.
- **CLI:** `gowe submit --workspace-upload` takes `--workspace-url` (or
  `GOWE_WORKSPACE_URL`), retries with a fresh node per attempt, and cleans up on Ctrl-C.
- **Stage-out streams from disk** — no whole-file buffering, so multi-GB outputs no longer
  need equivalent RAM on the server. The PUT is bounded by a progress watchdog
  (5 minutes without progress), not a total timeout.
- **Text outputs are Shock-backed too.** `Workspace.get` on a staged-out object returns
  the Shock node URL as its `data`, not the content; anything that read `_gowe_outputs.json`
  inline out of `Workspace.get` must follow the URL (or use `get_download_url`). GoWe's
  own stage-in path is unaffected.

### Admin recovery of Workspace outputs (#180)

Two admin-only endpoints and matching CLI commands verify and repair Workspace deliveries
(request/response detail in [`API_GUIDE.md`](API_GUIDE.md) §10):

| Operation | What it does |
|-----------|--------------|
| `gowe admin verify-outputs <id>` / `--all [--output-state ...] [--since YYYY-MM-DD]` | `POST /api/v1/admin/submissions/{id}/verify-outputs` — read-only; downloads every `ws://` output (child submissions included) and compares it to the sha1 checksum and size the worker recorded before upload. |
| `gowe admin redeliver <id> [--dry-run]` | `POST /api/v1/admin/submissions/{id}/redeliver` — locates the local original of each failing file by checksum+size among the task outputs, re-uploads it through the verified stager to the same path, re-verifies, rewrites the manifest, and sets `delivered` (restoring `COMPLETED` for an `OUTPUT_STAGING_FAILED` failure) only when every output verifies. |

Requirements and limits:

- The server must be started with **`--redeliver-source-dirs <dir>[,<dir>...]`**, the
  allowlist of local directories originals may be read from (typically the shared
  stage-out directory, e.g. `/scout/wf/data`). Symlinks are resolved and only regular files
  of the recorded size are opened. Without the flag `redeliver` refuses every local
  re-upload (originals report `original_missing`).
- Workspace staging must be configured (`--workspace-staging server`), otherwise `503`.
- Both operations use the **submission's stored token only** — the admin's token has no
  write access to another user's home. A missing or expired stored token yields `409`; the
  owner must re-submit.
- Only terminal submissions with `output_state` `delivered` or `upload_failed` are
  accepted (`409` otherwise, including `uploading`). One re-delivery per submission at a
  time; the final write is compare-and-set (`409` on a concurrent change).
- Nothing is ever deleted from the Workspace.

**Recommended post-upgrade procedure** for deployments that delivered outputs to a
Workspace before 0.15.0 — the local originals must still exist in the stage-out
directory (task-dir cleanup does not touch it):

```bash
# 1. Restart the server with the allowlist (plus your existing flags)
gowe-server ... --workspace-staging server --redeliver-source-dirs /scout/wf/data

# 2. Find every affected submission (read-only; exits non-zero when anything mismatches)
gowe admin verify-outputs --all --output-state delivered,upload_failed
gowe admin verify-outputs --all --output-state delivered,upload_failed --json > verify.json

# 3. For each submission with mismatches: plan, then repair
gowe admin redeliver sub_abc --dry-run     # would_reupload / original_missing per file
gowe admin redeliver sub_abc               # re-upload, re-verify, mark delivered

# 4. Confirm
gowe admin verify-outputs sub_abc
```

Outputs whose originals are gone (`original_missing`) cannot be repaired; regenerate them
by re-running the submission.
