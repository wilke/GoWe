# BV-BRC Submission & Workspace Deep Dive

> **Status**: Reference · **Date**: 2026-07-06
> **Scope**: How a workflow step is submitted to BV-BRC, how results return, how the BV-BRC
> Workspace (`ws://`) is used, and precisely why `ws://` is labelled **defined** rather than
> **supported** in [`SPECIFICATION.md`](../SPECIFICATION.md) §10 and [`Execution-Modes.md`](Execution-Modes.md).
>
> **Note on code references**: `file.go:NNN` line numbers below are a snapshot taken against
> `main` at authoring time and will drift as the code changes. Treat the enclosing
> **function/symbol name** as the durable reference and the line number as a hint.

## TL;DR

The BV-BRC + `ws://` path is **implemented end to end** — submission, status polling, output
mapping, logs, cancel, the workspace stager, and server-side pre/post-staging are all present
with no stubs or TODOs. It is labelled **defined** (not **supported**) because it is **not
verified by the automated conformance suite** — it requires a live BV-BRC service and a real
user token, which CI does not have — and because it carries a few functional edges that only
surface against real infrastructure (§5).

"Supported" in the storage taxonomy means *green in the conformance matrix on every commit*
(`local`, `file://`). "Defined" means *implemented but not CI-verified*.

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
  uploads local `file://` outputs back to `ws://` (via `ensureDir` + `WorkspaceUpload`) and
  writes a `_gowe_outputs.json` manifest.

Enabled with `--workspace-staging server --workspace-url <url>` (`cmd/server/main.go:41–42`).

The stager itself is complete — `StageIn` (`workspace.go:83`), `StageOut` (`workspace.go:125`),
`UploadContent` (`workspace.go:248`), `ensureDir` (`workspace.go:304`), and token resolution
(`workspace.go:345`) — with retries and no stubs.

### Client surface

[`pkg/bvbrc/workspace.go`](../pkg/bvbrc/workspace.go) implements `WorkspaceLs`, `Get`,
`Create`, `CreateFolder`, `Upload`, `Delete`, `Copy`, `Move`, `SetPermissions`,
`ListPermissions`, and `GetDownloadURL`. [`pkg/bvbrc/appservice.go`](../pkg/bvbrc/appservice.go)
implements `enumerate_apps`, `query_app_description`, `start_app`, `query_tasks`,
`query_task_details`, `kill_task`, `query_app_log`, and more.

---

## 4. Verification status

| Check | State | Evidence |
|-------|-------|----------|
| Conformance suite covers `server-bvbrc` / `ws://` | **No** (❌) | [`Execution-Modes.md`](Execution-Modes.md) matrix, line 72 |
| BV-BRC integration test exists | Yes, but gated | [`internal/executor/bvbrc_integration_test.go`](../internal/executor/bvbrc_integration_test.go) — `//go:build integration`, `skipIfNoBVBRC` skips without a live `BVBRC_TOKEN` |
| Workspace stager tests hit a real service | **No** | `pkg/staging/workspace_test.go` is unit-only (URI parsing, token priority) |
| Scheduler test exercises phase 1.5 / 5.5 | **No** | `internal/scheduler/integration_test.go` uses the local executor and never sets a workspace stager |

**Why it cannot run green in CI:** the path needs a live BV-BRC App Service **and** Workspace,
a valid non-expired user token, and a writable shared workspace — none of which exist in CI. So
the matrix cell cannot be marked ✅ regardless of code quality.

To exercise it manually:

```bash
export BVBRC_TOKEN="un=...|tokenid=...|expiry=...|..."
go test -tags=integration -v ./internal/executor/ -run TestBVBRCIntegration
```

---

## 5. Functional edges (what "defined" is honestly signalling)

Beyond the verification gap, these edges only surface against real infrastructure:

| Edge | Detail | Location |
|------|--------|----------|
| **Wildcard globs unresolved (fallback)** | When an app does not populate `output_files` **and** an output is a wildcard (`*.fasta`), the glob fallback deliberately skips it — resolving it would require a `WorkspaceLs` of the result folder, which the fallback does not do. The primary `output_files` path is unaffected. | `bvbrc.go:316` |
| **Whole-file-in-memory staging** | `StageOut` does `os.ReadFile` and uploads the entire content as a string — memory-bound for multi-GB genomics files. | `workspace.go:156` |
| **No recursive directory download** | `StageIn` is single-file; a `ws://` `Directory` output that a local step needs is not recursively listed and downloaded. | `workspace.go:83` |
| **No submit-time schema validation** | `query_app_description` exists but is not called; bad params fail at BV-BRC, not pre-flight. | `bvbrc.go:95` |

**The use case that is not proven:** a **hybrid** run that pulls `ws://` inputs down, executes
locally / in a container, and pushes outputs back to the Workspace — verified automatically.
The pure Role-A path (BV-BRC → BV-BRC) is most likely fine; the Role-B boundary-crossing path
with large files, wildcard outputs, or directory outputs is exactly where the untested edges
live.

**Bottom line:** the code is all there. What is missing is automated proof and a handful of
edge-case behaviours under real load. That is the difference between **defined** and
**supported**.

---

## Related

- [`SPECIFICATION.md`](../SPECIFICATION.md) §8 (executors), §10 (inputs & storage), §7 (scheduler phases)
- [ADR-0007](adr/0007-generic-bvbrc-executor-over-operators.md) — generic BV-BRC executor
- [ADR-0009](adr/0009-delegated-identity-and-optional-worker-keys.md) — delegated identity
- [`Execution-Modes.md`](Execution-Modes.md) — the test matrix and status legend
- [`BVBRC-App-Output-Convention.md`](BVBRC-App-Output-Convention.md) — the `.<output_file>` result-folder convention
- [`BVBRC-API.md`](BVBRC-API.md) — the JSON-RPC surface
