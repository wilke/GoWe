# BV-BRC Submission & Workspace Deep Dive

> **Status**: Reference · **Date**: 2026-09-02
> **Scope**: How a workflow step is submitted to BV-BRC, how results return, how the BV-BRC
> Workspace (`ws://`) is used, and what closed the gaps that promoted `ws://` from **defined**
> to **supported** in [`SPECIFICATION.md`](../SPECIFICATION.md) §10 and
> [`Execution-Modes.md`](Execution-Modes.md) (GoWe#154, GoWe#198).
>
> **Note on code references**: `file.go:NNN` line numbers below are a snapshot taken against
> `main` at authoring time and will drift as the code changes. Treat the enclosing
> **function/symbol name** as the durable reference and the line number as a hint.

## TL;DR

The BV-BRC + `ws://` path is **implemented end to end** — submission, status polling, output
mapping, logs, cancel, the workspace stager, and server-side pre/post-staging are all present
with no stubs or TODOs, and is now labelled **supported**. The three closed gaps (streamed,
size-verified upload; wildcard-glob output resolution via a result-folder listing; recursive
`Directory` stage-in) are covered by unit tests against a fake Workspace service and by a
gated hybrid round-trip integration test — `ws://` input → local/worker step → `ws://` output —
run against a live BV-BRC service (§4, §5).

"Supported" in the storage taxonomy elsewhere means *green in the conformance matrix on every
commit* (`local`, `file://`). `ws://` cannot meet that bar — a live BV-BRC service and a real
user token are not available to CI — so its promotion instead rests on the gated round-trip
having been run and its output attached as evidence on the promoting PR, not on a continuously
green matrix cell. One edge remains deliberately unclosed and optional: submit-time schema
validation against `query_app_description` (§1, GoWe#154 gap 4) — bad parameters still fail at
BV-BRC rather than in a pre-flight check.

---

## 1. Submission path

Routing selects the `bvbrc` executor when a step carries `gowe:Execution.executor: bvbrc` or a
`bvbrc_app_id` (see `SPECIFICATION.md` §8.2 and [ADR-0007](adr/0007-generic-bvbrc-executor-over-operators.md)).
`Submit()` in [`internal/executor/bvbrc.go`](../internal/executor/bvbrc.go) then:

1. **Resolves the app id** — `task.BVBRCAppID`, falling back to the legacy `_bvbrc_app_id`
   input (`bvbrc.go:96–104`).
2. **Chooses the caller / identity** — `getTaskCaller` (`bvbrc.go:60–85`) builds a per-task RPC
   caller from the submitter's own token (`RuntimeHints.StagerOverrides.HTTPCredential.Token`),
   falling back to a default server caller. This is the delegated identity of
   [ADR-0009](adr/0009-delegated-identity-and-optional-worker-keys.md): **the job runs as the
   user**.
3. **Flattens inputs to workspace paths** — `resolveBVBRCInput` (`bvbrc.go:513–540`) walks the
   CWL inputs and converts every `File`/`Directory` object to a plain workspace path string,
   recursing into record parameters (e.g. `paired_end_libs`). BV-BRC's Perl apps expect
   `/user@bvbrc/home/...` strings, **not** CWL objects — passing a raw object yields
   *"File HASH(0x...) does not exist"*.
4. **Submits asynchronously** — `AppService.start_app([appID, params, workspacePath])`
   (`bvbrc.go:143`) returns a **job UUID immediately** (`bvbrc.go:159–174`); the job runs on
   BV-BRC while GoWe polls.

> **Not called at submit time:** `query_app_description` / `enumerate_apps` exist in the client
> ([`pkg/bvbrc/appservice.go`](../pkg/bvbrc/appservice.go)) but `Submit()` does not invoke them.
> Parameters are trusted from the CWL tool, so a schema mismatch fails at BV-BRC rather than in a
> pre-flight validation step. ADR-0007 describes dynamic schema fetching as the intended design;
> the submit path does not yet exercise it.

---

## 2. Result return

Async executors are polled each scheduler tick (`SPECIFICATION.md` §7 phase 3). `Status()`
(`bvbrc.go:177–246`):

1. Calls `AppService.query_tasks([externalID])` (`bvbrc.go:193`) → `status`, `output_files`
   (`[[ws_path, uuid], …]`), and `parameters`.
2. `mapBVBRCState` (`bvbrc.go:568–581`): `completed → SUCCESS`,
   `failed`/`deleted`/`suspended` → `FAILED`, `in-progress → RUNNING`, `queued → QUEUED`.
3. On success, `buildOutputs` (`bvbrc.go:250–289`) constructs a `result_folder` **`Directory`**
   at `ws://{output_path}/.{output_file}` plus **`File`** objects with `ws://` locations,
   matched to declared CWL output ids by glob. If `output_files` is empty,
   `buildOutputsFromGlobs` (`bvbrc.go:292–327`) reconstructs outputs from the tool's glob
   patterns.
4. **Logs**: `query_task_details` → stdout/stderr URLs, fetched over HTTP with
   `Authorization: OAuth <token>` (`bvbrc.go:448–511`). **Cancel**: `kill_task`
   (`bvbrc.go:429–445`).

A completed BV-BRC job thus becomes CWL outputs whose bytes live **in the workspace**,
referenced by `ws://` URIs — not copied anywhere by default.

---

## 3. How the Workspace is used

The BV-BRC Workspace plays two distinct roles, and the distinction is the heart of the label.

### Role A — the Workspace *is* the storage (pure BV-BRC)

For an all-BV-BRC workflow, apps read inputs from workspace paths and write outputs back to
workspace paths. GoWe threads `ws://` path strings in (§1 step 3) and receives `ws://`
references back (§2 step 3). **GoWe never moves the bytes**, and the `ws://` *stager* is not
even invoked. This is the simplest path and the most likely to work unmodified.

### Role B — the stager crosses the boundary (hybrid workflows)

When BV-BRC data must meet a `local` / `worker` / container step, the stager in
[`pkg/staging/workspace.go`](../pkg/staging/workspace.go) moves the bytes, driven by the
server-side staging phases in [`internal/scheduler/workspace.go`](../internal/scheduler/workspace.go):

- **Phase 1.5 pre-stage** — `prestageWorkspaceInputs` (`scheduler/workspace.go:31–102`)
  downloads `ws://` inputs to local `file://` (via `WorkspaceGetDownloadURL` + HTTP GET with
  OAuth) and rewrites the input locations so a container step can read them.
- **Phase 5.5 post-stage** — `poststageWorkspaceOutputs` (`scheduler/workspace.go:106–198`)
  uploads local `file://` outputs back to `ws://` (via `ensureDir` + `WorkspaceUploadFile`,
  which creates the object as a Shock upload node and PUTs the raw bytes to it) and writes a
  `_gowe_outputs.json` manifest.

Enabled with `--workspace-staging server --workspace-url <url>` (`cmd/server/main.go:41–42`).

The stager itself is complete — `StageIn` (`workspace.go:83`), `StageOut` (`workspace.go:125`),
`UploadContent` (`workspace.go:248`), `ensureDir` (`workspace.go:304`), and token resolution
(`workspace.go:345`) — with retries and no stubs.

### Client surface

[`pkg/bvbrc/workspace.go`](../pkg/bvbrc/workspace.go) implements `WorkspaceLs`, `Get`,
`Create`, `CreateFolder`, `UploadFile`, `Delete`, `Copy`, `Move`, `SetPermissions`,
`ListPermissions`, and `GetDownloadURL`.

`WorkspaceUploadFile` is the **only** upload entry point, and it always goes through Shock:
`Workspace.create` with `createUploadNodes` and `overwrite`, then a multipart `PUT` of the
raw bytes to the Shock URL in `ObjectMeta[11]`, with `Authorization: OAuth <token>` — the
protocol `scripts/ws-create.pl` implements. The inline `Content` field of `Workspace.create`
is reserved for folders and upload-node placeholders (`null`); routing file bytes through it
corrupted every binary output, because it is a JSON string and `encoding/json` replaces every
byte that is not valid UTF-8 with U+FFFD. That was issue #172; there is a regression test
pinning it in `pkg/bvbrc/workspace_test.go`.

A consequence worth knowing when reading results back: because **every** staged-out file —
including plain text such as logs, JSON, TSV — is now Shock-backed, `Workspace.get` on a
stage-out object no longer returns its content inline. The data half of the `[meta, data]`
pair is the Shock node URL (the same string as `ObjectMeta[11]`); the bytes must be fetched
from Shock or via `Workspace.get_download_url`. The recorded pair in
`pkg/bvbrc/testdata/workspace/get.json` shows both cases side by side: an inline object
(`data` is `"hello\n"`) and a Shock-backed one (`data` is the node URL). Anything that
greps a text output by calling `Workspace.get` and reading `data` has to follow the URL.
The one exception is a **0-byte** stage-out file: the streaming uploader routes size 0
through the inline text path (`Workspace.create` with `""`), so an empty output has no
Shock node and `Workspace.get` returns `""` as its data.

[`pkg/bvbrc/appservice.go`](../pkg/bvbrc/appservice.go)
implements `enumerate_apps`, `query_app_description`, `start_app`, `query_tasks`,
`query_task_details`, `kill_task`, `query_app_log`, and more.

---

## 4. Verification status

| Check | State | Evidence |
|-------|-------|----------|
| Conformance suite covers `server-bvbrc` / `ws://` | **No** (❌) | [`Execution-Modes.md`](Execution-Modes.md) matrix — `ws://` is `✅*` (gated integration test), not `✅` (conformance suite) |
| BV-BRC integration test exists | Yes, gated | [`internal/executor/bvbrc_integration_test.go`](../internal/executor/bvbrc_integration_test.go) — `//go:build integration`, `skipIfNoBVBRC` skips without a live `BVBRC_TOKEN`. Includes `TestBVBRCIntegration_SubmitAndPoll` (submit/poll/logs), `TestBVBRCIntegration_WorkspaceDirectoryRoundTrip` (recursive `Directory` stage-out then stage-in, byte-for-byte), and `TestBVBRCIntegration_WildcardGlobOutputResolution` (a real completed job's result folder resolved through a wildcard array glob and a wildcard scalar glob) |
| Workspace stager tests hit a real service | Partially | `pkg/staging/workspace_test.go` / `workspace_stageout_test.go` / `workspace_stagein_test.go` exercise the full upload/download protocol against the `pkg/bvbrc/bvbrctest` fake (URI parsing, token priority, streamed upload, recursive directory download, entry/depth caps); the `-tags=integration` tests above are what hits the real service |
| Scheduler test exercises phase 1.5 / 5.5 | Partially | `internal/scheduler/workspace_attribution_test.go` exercises both phases — including a `ws://` `Directory` input recursively pre-staged against `bvbrctest` — against hand-rolled/fake HTTP servers, not a live service; `internal/scheduler/integration_test.go` still uses the local executor only |

**Why it cannot run green in CI:** the path needs a live BV-BRC App Service **and** Workspace,
a valid non-expired user token, and a writable shared workspace — none of which exist in CI. So
the matrix cell is marked `✅*` (verified, gated) rather than `✅` (conformance-suite-verified),
and promotion to *supported* is an evidence-attached-to-the-PR event, not a CI state.

To exercise it manually:

```bash
export BVBRC_TOKEN="un=...|tokenid=...|expiry=...|..."
go test -tags=integration -v ./internal/executor/ -run TestBVBRCIntegration
```

---

## 5. What "supported" required (GoWe#154, GoWe#198)

Three functional edges stood between "the code is all there" and a Role-B (boundary-crossing)
run actually working end to end. All three are now closed:

| Edge | Resolution | Location |
|------|------------|----------|
| ~~**Whole-file-in-memory staging**~~ (resolved, PR #177) | `StageOut` streams the file from disk through `WorkspaceUploadReader` with an exact `Content-Length` (never chunked), a progress watchdog, and size/md5 verification against the Shock reply; verified against the live service by the `pkg/bvbrc` integration tests. See [`SPECIFICATION.md`](../SPECIFICATION.md) §10.6. | `pkg/staging/workspace.go` `uploadFile` |
| ~~**Wildcard globs unresolved (fallback)**~~ (resolved, GoWe#154) | When an app does not populate `output_files` **and** an output's glob contains `*?[`, `buildOutputsFromGlobs` now lists the result folder once via `Workspace.ls` (`bvbrcpkg.WorkspaceLs`) and matches each wildcard glob against the listing with the existing `globMatches` helper. An array-typed output (`File[]`, `File[]?`, `{"type":"array",...}`, or a union containing one) collects every match, sorted; a scalar output requires exactly one match and errors — the same way any other `Status()` failure on this path does — when more than one file matches. An empty listing (the result folder can still be indexing right after job completion) is tolerated: the wildcard outputs are left unresolved and a `Warn` is logged, not an error; concrete (non-wildcard) globs are unaffected either way. | `internal/executor/bvbrc.go` `buildOutputsFromGlobs`, `listResultFolder` |
| ~~**No recursive directory download**~~ (resolved, GoWe#154) | `StageIn` now detects whether a `ws://` location is a folder (`WorkspaceGet` metadata-only — see caveat below) and, if so, recursively lists and downloads its whole tree via `stageInDirectory`, preserving the relative path structure under `destPath` and reusing the existing per-file `stageInFile`/`download` (with its #215-style size verification, unmodified). The walk is capped at 10 000 total entries and 20 levels of depth, checked before any per-level work so a directory that blows a cap does no partial mkdir/download; it also propagates `ctx` cancellation between levels. `internal/scheduler/workspace.go`'s `prestageWorkspaceInputs` needed one small change to stay correct under this: `walkLocations` no longer also queues a Directory's own pre-populated `listing` entries as separate top-level downloads once it has queued the Directory's own `location` — that would have duplicated the transfer and flattened nested files out of the tree `stageInDirectory` had just reconstructed. | `pkg/staging/workspace.go` `StageIn`, `isFolder`, `stageInDirectory`; `internal/scheduler/workspace.go` `walkLocations` |
| **No submit-time schema validation** *(intentionally still open, GoWe#154 gap 4)* | `query_app_description` exists but is not called; bad params fail at BV-BRC, not pre-flight. Out of scope for this promotion — noted here so it isn't mistaken for an oversight. | `bvbrc.go:95` |

**Caveat on the folder-vs-file check:** `StageIn`'s `isFolder` helper uses a metadata-only
`Workspace.get` on the path itself, not a `Workspace.ls` of its parent — a deliberate deviation
from a literal reading of the original issue text, chosen because a missing object then comes
back as a clear "not found" error (the same pattern `deletePlaceholder` already relies on)
rather than an ambiguous empty listing. Whether real BV-BRC accepts `Workspace.get` on a folder
path the way `pkg/bvbrc/bvbrctest`'s fake does is exactly what
`TestBVBRCIntegration_WorkspaceDirectoryRoundTrip` (§4) verifies; if it doesn't, `isFolder` is a
single, isolated function to swap for a `Workspace.ls`-based check instead.

**The use case that is now proven:** `TestBVBRCIntegration_WorkspaceDirectoryRoundTrip`
uploads a small nested tree (including a subfolder) into a scratch Workspace folder file-by-file
through `WorkspaceStager.StageOut` — the same code server-side post-staging uses — and then
recursively downloads it back with one `StageIn` call, diffing the two trees byte-for-byte.
`TestBVBRCIntegration_WildcardGlobOutputResolution` submits the `Date` app, waits for it to
complete, and calls `buildOutputsFromGlobs` directly against the real result folder with a
synthetic tool definition carrying a wildcard array glob (`*`) and a wildcard scalar glob
(`date.*`) — calling it directly, rather than depending on whatever `Date`'s own CWL wrapper
declares, sidesteps needing an app whose `output_files` BV-BRC happens to leave empty (the
condition that triggers this code path in production) while still exercising it against a real
completed job and a real `Workspace.ls`. Both tests are `//go:build integration`-gated and
require a live `BVBRC_TOKEN`; see §4 for how to run them.

**Bottom line:** the code is all there, the three real functional gaps are closed, and both are
now exercised by a fake-service unit-test seam (`pkg/bvbrc/bvbrctest`) for every commit and by a
gated live round-trip for promotion evidence. Gap 4 (schema pre-flight) is the one edge left
open, by design.

---

## Related

- [`SPECIFICATION.md`](../SPECIFICATION.md) §8 (executors), §10 (inputs & storage), §7 (scheduler phases)
- [ADR-0007](adr/0007-generic-bvbrc-executor-over-operators.md) — generic BV-BRC executor
- [ADR-0009](adr/0009-delegated-identity-and-optional-worker-keys.md) — delegated identity
- [`Execution-Modes.md`](Execution-Modes.md) — the test matrix and status legend
- [`BVBRC-App-Output-Convention.md`](BVBRC-App-Output-Convention.md) — the `.<output_file>` result-folder convention
- [`BVBRC-API.md`](BVBRC-API.md) — the JSON-RPC surface
