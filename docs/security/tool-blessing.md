# Tool blessing

Status: **infrastructure only.** Blessings can be created, listed, extended and
revoked (REST, `gowe admin`, and the `/admin/tool-blessings` UI page), but no
code consults them yet. Token injection, submission-time checks and the
scheduler are unchanged by this change. The policy wiring (what a missing
blessing does, `--token-blessing=enforce|dev`) is documented when it lands.

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
`worker_groups` is an optional extra restriction on where the tool may run. The
worker-side digest check (verify the resolved image file before injecting) is
part of the later enforcement change.

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
