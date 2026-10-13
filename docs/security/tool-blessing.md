# Tool blessing

Status: **enforced end to end.** The scheduler grants the user's token to a tool
only if the tool requests it and an active blessing covers its exact content
(see "Policy" below), and the worker refuses to inject unless the task carries
the blessing id and the resolved image matches a blessed digest (see "Worker
side"). `--token-blessing=dev` is the loud development escape hatch.

## Why

A user's BV-BRC token is a full-account credential: it has no audience claim
(it is valid at GoWe, Workspace and Shock alike) and cannot be revoked at
BV-BRC before it expires. A tool that receives it in its environment can do
anything with it. Blessing is the explicit, reviewed decision that *this exact
tool, in this exact image*, may hold it.

## What is blessed

A blessing is keyed by the **tool hash**: `sha256:` + hex(sha256(`v1\n` +
canonical JSON of the final tool document)). The document is the
CommandLineTool after workflow, step and job requirements have been merged in
(`internal/toolmerge`, the same code the scheduler runs), so what an
administrator reviews at bless time is what would run.

Canonicalisation (`internal/toolhash`):

- Top-level `id`, `cwlVersion`, `$namespaces`, `$schemas` are dropped, so a
  standalone tool, a packed `$graph` copy and a `gowe://`-inlined copy hash the
  same.
- A fixed set of classes that cannot change what code runs is dropped from
  both `requirements` and `hints`: `ScatterFeatureRequirement`,
  `StepInputExpressionRequirement`, `SubworkflowFeatureRequirement`,
  `MultipleInputFeatureRequirement`, `ResourceRequirement`,
  `SchemaDefRequirement`, `LoadListingRequirement`, `WorkReuse`,
  `ToolTimeLimit`, `gowe:ResourceData`, and `InlineJavascriptRequirement`
  *without* an `expressionLib`.
- **Everything else is hashed** (default-deny): `DockerRequirement`,
  `InitialWorkDirRequirement`, `EnvVarRequirement`, `ShellCommandRequirement`,
  `NetworkAccess`, `gowe:Execution`, `cwltool:*`, unknown classes, and any
  `expressionLib`. A workflow that adds, say, an `EnvVarRequirement` to a
  blessed tool therefore produces a different hash.
- Deterministic JSON (sorted keys). The image reference
  (`DockerRequirement.dockerPull`) is part of the document and so of the hash.

Not covered by the hash: *blank-check* tools whose executable content comes
from inputs (`$(inputs.cmd)`, an IWDR entry of `$(inputs.script)`). They change
the job, not the tool. The review flags exist to catch them at bless time.

## Pinned images (required)

A name is not bytes. A blessing therefore holds a **list of image sha256
digests** (`image_sha256s`, at least one, hex without prefix). There is no
unpinned option and no expiry. The same recipe built on another host yields a
different digest, so add each host build:

```bash
gowe admin bless bvbrc-helper --image-file /scout/containers/python.sif
gowe admin blessings add-image <id> --image-file /other/host/python.sif
```

`docker://` references used by a blessed tool must be `@sha256:`-pinned.
`worker_groups` is an optional extra restriction on where the tool may run.

## Worker side

The scheduler stamps a blessed task with `TokenBlessingID`, `TokenEnvNames` and
the blessing's `ImageSHA256s`. The worker injects the token only when the
blessing id is present, a credential exists, and the image it is about to run
matches one of those digests: it resolves the image as execution will
(`.sif` through `--image-dir` and symlinks), hashes the file, and launches the
same resolved path. Registry refs must be `@sha256:`-pinned and listed. A
mismatch fails the task before launch (error names the path, the digest and the
expected list). Without a blessing id the worker logs a WARN and runs the task
without the token. After the task the worker drops the token. Code:
`internal/worker/tokenpolicy.go`.

- **Hashing cost.** Images can be tens of GB, so the worker caches each
  digest in memory keyed on path, device, inode, size, mtime and ctime; an
  image is hashed once per worker process and again only if the file is
  replaced or modified.
- **TOCTOU residual.** The file is hashed and then launched as two steps. A
  swap in between requires write access to the image directory, i.e. the
  operator's uid; the image directory is operator-owned and out of the tool's
  reach. The worker does not refuse group/world-writable images (operators
  commonly keep shared image directories group-writable).
- **Registry refs.** For a `docker://` or Docker reference, the digest to bless
  is the manifest digest from the `@sha256:` in the ref (`gowe admin bless
  --image-sha256 <hex>`), not a hash of any file. The worker compares that
  digest and relies on the runtime to pull by it.

## Policy

- **Request**: only the tool document can ask for the token
  (`gowe:Execution.inject_bvbrc_token: true` under the tool's `hints`).
  Steps may route (`executor`, `worker_group`, `gpu`, secret opt-ins) but never
  request; `gowe:Execution` under `requirements` is a registration error.
- **Submit** (fast-fail, `400`): a step whose tool requests the token needs an
  active blessing for the digest the scheduler will compute (workflow, step and
  job requirements merged; nested sub-workflow steps against the child root;
  the error names the inherited class that broke a blessing) and an allowed
  worker group (`TOOL_NOT_BLESSED`); job `cwl:requirements` and step-level
  `DockerRequirement`/`gowe:Execution.docker_image` (requirements or hints) are
  refused (`TOKEN_TOOL_OVERRIDE_REFUSED`). Registration only warns.
- **Dispatch** (authoritative): the scheduler recomputes the digest of the final
  `task.Tool`, requires the worker executor, an active blessing and an allowed
  group, then sets `InjectBVBRCToken`, `TokenBlessingID`, `TokenEnvNames`,
  `ImageSHA256s`. Any denial fails the task before it is queued
  (`gowe_token_grants_total{workflow,outcome}`: granted, granted_dev,
  denied_unblessed, denied_group, denied_executor). Retries redo the check, so a
  revoked blessing stops a retry.
- **Dev mode** (`gowe-server --token-blessing=dev`, default `enforce`): an
  unblessed request is granted with `TokenBlessingID="dev-mode"`, the default
  env names, and the sha256 of the tool's `.sif` computed from
  `--dev-image-dir` (the worker still verifies it). Anything else (registry
  image, no dir, missing file) is denied. Every grant logs a WARN, `/api/v1/health`
  reports `token_blessing: "dev"`, the UI shows a banner and the submission
  response carries a warning naming the tool. Blessed tools behave identically in
  both modes; nothing else is relaxed.
- **BV-BRC executor**: a token request on a tool that runs on the BV-BRC
  executor is a no-op, not a denial: that executor sends the submitter's token
  as a request header and never places it in a tool environment.
- **Migration**: bless the three `bvbrc-*` helper tools (`gowe admin bless`)
  before submitting `genome-analysis-pipeline`, but only after every worker in
  the groups the blessing allows runs 0.22 (or restrict `worker_groups` to
  groups already rolled). A 0.21 worker that checks out a blessed task injects
  the token on the old gate: no image digest check, no `--containall
  --cleanenv`, no env allow-list, no post-task scrub.

## Lifecycle

- `inspect` computes the hash and review report without recording anything.
- `bless` recomputes the hash from the registered CWL (a supplied `tool_hash`
  that disagrees is refused) and stores the blessing with a summary snapshot.
  At most one *active* blessing exists per hash.
- `unbless` sets `revoked_at/by/reason`; the row stays for audit, and the hash
  can be blessed again afterwards.
- Every action is logged (`AUDIT tool blessing ...`, including who) and counted
  in `gowe_tool_blessing_events_total{action}`.

## Review checklist

`inspect` and `gowe admin bless` print every InitialWorkDirRequirement script
verbatim plus automatic flags. Blessing proceeds only after the reviewer
confirms (CLI prompt, UI acknowledgement box). Check:

1. **Read every script.** Does it only do what the tool claims? Does it send
   data anywhere (`NetworkAccess` is flagged; a blessed tool can still post the
   token over its own connection)?
2. **Output handling** (mandatory). Declared `stdout:` / `stderr:` outputs are
   staged out to the user's destination. `stderr` is where tracebacks and HTTP
   error bodies land, so a tool that captures stderr into a declared output
   file is the one most likely to ship the token into the workspace. Flags:
   - `declared-stdout` / `declared-stderr`: a declared stream output;
   - `stream-redirection`: `ShellCommandRequirement` with `2>&1`, `>file`,
     `&>`, `|&` and similar, or a shell script doing so;
   - `verbose-debug`: `--verbose`, `--debug`, `-v`, `set -x`, debug logging
     (verbose HTTP clients print request headers, including `Authorization`);
   - `prints-env-or-headers`: `env`, `printenv`, printing `os.environ`, or
     printing/logging headers;
   - `input-derived-executable`: baseCommand, shell arguments or IWDR content
     derived from inputs;
   - `network-access`.
3. **Image.** Is `image_ref` the image you expect, and does the digest you pin
   match the file you reviewed?
4. **Scope.** Restrict `env_names` to what the tool reads; set `worker_groups`
   when the tool should only run on specific workers.

Residual (accepted): redaction of outputs is value-based and not part of this
change; a blessed tool can still write the token into any file or send it over
the network. Blessing is the trust decision about the tool's code.

## Where to look

- Model: `pkg/model/tool_blessing.go`; hints fields `RuntimeHints.TokenEnvNames`,
  `ToolHash`, `TokenBlessingID`, `ImageSHA256s` (the blessing's whole digest list; metadata, survive `ScrubSecrets`).
- Hashing and review summary: `internal/toolhash`; shared merge code:
  `internal/toolmerge`; admin logic: `internal/blessing`.
- Storage: `blessed_tools` table, `internal/store/tool_blessings.go`.
- API: [API_GUIDE.md](../API_GUIDE.md) section 11; CLI `gowe admin bless`.

## Residuals

- `ReconcileWorkerTasks`/`RequeueWorkerTasks` (worker loss, heartbeat reconcile)
  reset RUNNING tasks to QUEUED without re-running the credential policy, so a
  blessing revoked mid-flight is re-checked only on a real retry
  (`attachCredentials`) or a new submission. The store guard still refuses a
  live task with the inject flag and no blessing id.
- Pre-policy rows: at startup, non-terminal rows with the inject flag and no
  blessing id lose the request (they run without the token); `CheckoutTask`
  never hands such a row to a worker.
