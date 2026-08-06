# Worker Isolation — Security Analysis & Hardening Plan

**Status:** Advisory / proposal — no code changes made
**Scope:** How GoWe executes CWL tasks, and what changes when workers run **untrusted** code
**Verified against:** `main` @ `0c50df7` (v0.13.2)
**Audience:** GoWe engineering team, Claude Code

---

## 1. Bottom line

GoWe's execution design is sound, and the **`toolexec` path is already meaningfully hardened** — network isolation and resource limits are implemented and driven by the CWL spec. But verification surfaced two issues that are **live defects today, not just untrusted-code concerns**, plus five gaps that block untrusted execution.

**Live today, under the current trusted deployment:**

1. **Secrets are logged in the clear.** They are passed on argv (`-e NAME=value`) and both arg vectors are `Debug`-logged. `docker-compose.yml` runs workers with `-debug`. This **violates `SPECIFICATION.md` §13.5** ("secret **values** MUST NOT be logged") and §13.2 — it is a compliance bug, not a hardening suggestion.
2. **In `DockerVolume` mode every tool container can read and write every other task's workdir.** The volume-mount-point walk resolves to the volume root, so `gowe-workdir` is mounted RW into each container. Cross-task data access exists **now**.

**Blockers for untrusted code:**

3. **The tool declares its own network policy.** `NetworkAccess: true` in the submitted CWL lifts `--network none` — settable via `hints` as well as `requirements`.
4. **Containers run as root.** There is no `--user` anywhere in the codebase.
5. **Apptainer inherits the host environment** — no `--containall` / `--cleanenv`.
6. **Pre-staged datasets and IWDR mounts are read-write**, so a tool can modify shared reference data.
7. **A second, unhardened execution path exists** that bypasses every `toolexec` control — and creates host directories from user-controlled input.

Items 1 and 2 should be fixed regardless of the trust roadmap.

Fix in two layers:

- **Layer 0** — close the five gaps above in the existing backends. Cheap, mostly additive, improves trusted runs too.
- **Layer 1** — add a sandboxed executor backend (Kata / gVisor, one ephemeral pod per task), because neither Docker nor Apptainer is a sandbox against hostile input regardless of flags.

---

## 2. Scope & threat model

The deployment this analysis targets:

| Dimension | Value |
|---|---|
| Tenancy | Single-user tenant |
| Exposure | Internet-facing API + UI |
| Compute | Bioinformatics analysis **in-cluster** (ANL sequencing center / MG-RAST workloads) |
| Data | Some **embargoed / access-controlled** sequence data |
| Trust today | Trusted code |
| Trust planned | **Untrusted workflows** |

Two facts drive everything below:

- **The CWL `CommandLineTool` is the untrusted thing.** It wraps an arbitrary binary. Metagenomics parsers, assemblers and aligners are memory-unsafe C/C++ processing attacker-influenceable input.
- **The worker is trusted and credential-bearing.** It creates the workdir, resolves the image, injects environment and secrets, and starts the container. Whatever that binary can read or reach is the worker's exposure.

So "protect the workers" means two workstreams: **(A) sandbox the execution**, and **(B) stop the worker handing trust material to code it doesn't trust.** (B) needs no exploit and is the more urgent of the two.

---

## 3. How GoWe runs a task today

Flow: `Submission → StepInstance → Task`. Workers **pull** work (`GET /workers/{id}/work`), matched on container-runtime capability, `--group`, and dataset affinity. Execution then splits across **two distinct paths** — this distinction matters for everything that follows.

### 3.1 Two execution paths

| | Path A — `toolexec` (primary) | Path B — legacy executor (fallback) |
|---|---|---|
| Code | `internal/toolexec/execute.go` | `internal/executor/docker.go`, `apptainer.go`, `local.go` |
| Trigger | Full CWL tool/job present | `_base_command` + `_docker_image` in `task.Inputs` |
| Used by | Workers, `cwltool`, `cwl-runner` | Legacy/direct submission path |
| Network isolation | **Yes** — `--network none` by default | **No flag at all** |
| Resource limits | **Yes** — `--memory`, `--cpus` from `ResourceRequirement` | **None** |
| GPU handling | `--gpus` / `--nv` | None |
| Env/secret injection | `-e` / `--env` | None |

`internal/executor/docker.go` (`:104`) and `local.go` (`:64`) delegate to Path A when `task.HasTool()`, falling back to a legacy branch otherwise. **`apptainer.go` has no such delegation** — it only ever implements the unhardened legacy path.

### 3.2 What is verified correct

These properties were checked in source and hold:

| Property | Verdict | Evidence |
|---|---|---|
| Each task gets its own working directory | Confirmed | `os.MkdirAll(taskDir, 0o755)` in `docker.go:99`, `apptainer.go:70`; `workDir` + `workDir+"_tmp"` in `toolexec` |
| Environment is per-task, not static | Confirmed | Built per execution from `EnvVarRequirement` + worker config + GPU (`execute.go:82-102`, `398-414`, `622-645`) |
| Docker/Apptainer backends only *dispatch* | Confirmed | Build an args slice, then `exec.CommandContext(ctx, "docker"\|"apptainer", …)` |
| Worker creates workdir and starts the container with the command | Confirmed | `docker run … <image> <cmd>` / `apptainer exec … <image> <cmd>` |
| Docker socket not mounted into **tool** containers | Confirmed — but see F11 | No socket bind in any arg builder. **However** `docker-compose.yml:123` mounts it into the **worker** container. |
| ~~No shared-workdir cross-task persistence~~ | **RETRACTED — see F10** | True in bind-mount mode; **false** in `DockerVolume` mode, where the whole volume is mounted RW into every tool container. |
| Network isolation on the primary path | **Confirmed present** | `execute.go:276-278` (Docker), `663-665` (Apptainer) |
| Resource limits on the primary path | **Confirmed present** | `execute.go:267-272` (Docker), `650-657` (Apptainer, cgroups v2 only) |
| Image "download" is worker-controlled | **No** — implicit | No explicit pull; runtime pulls on-miss. `resolveApptainerImage()` prefers local `.sif`, else `docker://` |

---

## 4. Findings

Severity reflects impact **under the untrusted-code threat model**. Several are acceptable for the current trusted deployment.

### F1 — Untrusted tools can disable their own network isolation · **Critical**

Network isolation is implemented, but it is **conditional on a value read from the tool itself**:

```go
// internal/toolexec/execute.go:274-278
// Network isolation: disable network access unless NetworkAccess requirement enables it.
// CWL spec: NetworkAccess defaults to false (no network access).
if !hasNetworkAccess(tool) {
    dockerArgs = append(dockerArgs, "--network", "none")
}
```

`hasNetworkAccess(tool)` (`helpers.go:123-125` → `requirements.HasNetworkAccess`) inspects the **submitted CWL document**. A hostile workflow simply declares network access and receives full outbound egress.

**The accepted surface is wider than one field.** `internal/requirements/requirements.go:16-47` accepts **four spellings** — key `NetworkAccessRequirement` *or* `NetworkAccess`, under `tool.Requirements` *or* `tool.Hints`:

```yaml
# both of these work, and so do the *Requirement spellings
requirements: { NetworkAccess: { networkAccess: true } }
hints:        { NetworkAccess: { networkAccess: true } }
```

A gate that validates only `requirements:` is bypassed via `hints:` — the less-scrutinized channel. This is correct CWL behaviour and correct for trusted code; for untrusted code it is a **self-declared exfiltration path**, and the most direct route from a malicious workflow to embargoed sequence data leaving the cluster.

**Fix:** make network access a **policy decision, not a tool declaration**. For `trust=untrusted`, ignore all four spellings and force `--network none`. Enforce at the worker, not the server, since the worker builds the args.

Two secondary gaps in the same area:

- Apptainer's `--net --network none` "is silently ignored" without unprivileged userns support (`execute.go:661-662`). **Silent** failure of a security control is the wrong default for untrusted work — it must be a hard error.
- Path B (`executor/docker.go:141-149`, `apptainer.go:95-102`) sets **no network flag at all**, so anything routed there has network regardless.

### F2 — Containers run as root · **High**

No `--user` flag is set in any code path. Verified: `grep -rn '"--user"\|os.Getuid' internal/` returns nothing. The tool runs as the image's default user, typically **root**. Root in a default-config container sharing the host kernel means a container escape lands as **root on the worker node**.

```go
// internal/toolexec/execute.go:251 — no --user
dockerArgs := []string{"run", "--rm", "-i"}
```

**Fix:** `--user <uid>:<gid>` (non-root), plus `--security-opt no-new-privileges`. Note this interacts with output file ownership on bind mounts — needs a matching uid on the host workdir.

### F3 — Secrets are logged in the clear and exposed on argv · **High — active spec violation**

This is **not** a hardening suggestion. `SPECIFICATION.md` §13.5 states secret **values** "MUST NOT be logged", and §13.2 that a worker must treat secrets as process-local, "never echoed to the server **or to logs**." Both are violated today.

Secrets are appended as argv elements:

```go
// internal/toolexec/execute.go:412-414 (Docker)
for name, value := range opts.SecretEnvVars {
    dockerArgs = append(dockerArgs, "-e", name+"="+value)
}
// :635-637 (Apptainer) — same via --env
```

Argv is world-readable on Linux: any local process can read the secret from `ps auxww` or `/proc/<pid>/cmdline` while the container starts. The worker's `--secret` values are correctly never sent to the *server*, but they are exposed **locally on the worker host** at exec time.

**Both arg vectors are then logged in full:**

| Site | Logs | Secrets appended at |
|---|---|---|
| `execute.go:427` | `e.logger.Debug("docker command", "args", dockerArgs)` | `:412-414` |
| `execute.go:680` | `e.logger.Debug("apptainer command", "args", apptainerArgs)` | `:635-637` |

**This is on by default in the shipped dev deployment** — `docker-compose.yml` passes `-debug` to the worker services (lines 31, 63, 90, 117), and line 121 sets `DOCKER_VOLUME=gowe-workdir`.

**Fix:** pass secrets via `--env-file` (a `0600` file in the per-task dir, removed after); redact `-e`/`--env` pairs at **both** log sites; and for untrusted tasks, inject no secrets at all (§5.3).

**Reuse what exists:** `redactSecrets()` (`internal/worker/worker.go:508`) already scrubs secret values from captured stdout/stderr — the same helper should cover these two log sites. Note it **skips values shorter than 6 bytes** by design, so short secrets are never redacted even from tool output; worth revisiting alongside this fix.

### F4 — Apptainer inherits the host environment · **High**

The Apptainer arg builder sets `--home`, `--pwd`, mounts and `--env` values, but never `--containall` or `--cleanenv`. Apptainer's default behaviour is to **pass the host environment through** and bind-mount host locations. The worker process's environment — which may hold tokens, `S3_SECRET_KEY`, `BVBRC_TOKEN`, and the very secrets from F3 — becomes readable inside the container. **No escape required.**

`--home absWorkDir:containerWorkDir` (`execute.go:587`) does override the home mount, which helps, but does not sanitize the environment.

**Fix:** add `--containall` (or at minimum `--cleanenv --no-home` alongside the explicit `--home`), and construct the container environment explicitly from `EnvVarRequirement` + worker config.

### F5 — Local backend inherits the full worker environment · **High** (untrusted only)

```go
// internal/toolexec/execute.go:75
cmd.Env = os.Environ()
```

The `local` path executes the tool as a **direct child of the worker process** with the worker's entire environment and no container boundary whatsoever.

**Fix:** mark `local` ineligible for untrusted tasks at checkout; build `cmd.Env` from an explicit allow-list rather than inheriting.

### F6 — No capability, seccomp, read-only or PID limits · **Medium**

`--memory` and `--cpus` are set from `ResourceRequirement` (good — `execute.go:267-272`), but the Docker args include none of:

- `--cap-drop=ALL` (default Docker caps are a non-trivial set)
- `--security-opt seccomp=<profile>` / `no-new-privileges`
- `--read-only` + tmpfs scratch
- `--pids-limit` (fork-bomb guard)

Apptainer's `--memory`/`--cpus` are additionally gated on `opts.Resources.ApptainerCgroups` and skipped silently on systems without cgroups v2 — i.e. **most HPC** (`execute.go:648-657`).

### F7 — Implicit image resolution, no provenance verification · **Medium**

No explicit pull step exists. `resolveApptainerImage(dockerImage, opts.ImageDir)` prefers a local `.sif` and otherwise falls back to `docker://<image>`; Docker pulls on-miss. GoWe controls neither the source nor a signature, so whatever the runtime resolves is what runs — including a mutable tag re-pointed after review.

**Fix:** resolve to a pinned digest from an allow-listed registry; for Apptainer, run pre-verified SIFs (`apptainer verify` against a keyring) rather than live `docker://`.

### F8 — Two paths, one hardened · **Medium (architectural)**

Every control in §4 lives in `toolexec`. `internal/executor/docker.go` (legacy `_base_command` branch), `local.go`, and **all** of `internal/executor/apptainer.go` build their own args with no network flag, no limits, and no env controls:

```go
// internal/executor/docker.go:141-149
args := []string{
    "run", "--rm",
    "--name", containerName,
    "-v", taskDir + ":/work",
    "-w", "/work",
}
```

Any task reaching these paths bypasses the hardening entirely.

They are also **worse than "missing flags"**: both create host directories from user-controlled input and bind-mount them read-write. `docker.go:130-133` and `apptainer.go:85-88` call `os.MkdirAll(path, 0o755)` where `path` comes from a `Directory` input's `location` in `task.Inputs`, then mount `path:/work/<k>`:

```go
// internal/executor/apptainer.go:85-88
if err := os.MkdirAll(path, 0o755); err != nil { ... }
bindArgs = append(bindArgs, "--bind", path+":/work/"+k)
```

That is **arbitrary host-path creation and read-write exposure** driven by submission input. Additionally `apptainer.go:101` unconditionally uses `docker://`+image with no local `.sif` resolution — a stronger version of F7.

**Fix:** either route all container execution through `toolexec`, or make the legacy paths reject `trust=untrusted` outright. Prefer the latter as an immediate guard, the former as the real fix. Validate/allow-list `location` paths regardless of trust level.

### F9 — Neither runtime is a sandbox against hostile input · **Architectural**

Even fully hardened, Docker shares the **host kernel** and Apptainer runs as the invoking user with a shared filesystem. A single kernel-level escape crosses the boundary. Hardened args reduce risk; they do not create a sandbox. This is why Layer 1 exists.

### F10 — `DockerVolume` mode gives every task read-write access to every other task · **Critical (live today)**

An earlier draft of this document listed "no shared-workdir cross-task persistence" as verified-safe. **That was wrong**, and it is corrected here.

In bind-mount mode the claim holds — each task gets its own `taskDir`. But in `DockerVolume` mode the mount point is computed by walking `workDir` up to its **top-level component**:

```go
// internal/toolexec/execute.go:294-302
volumeMountPoint := workDir
for {
    parent := filepath.Dir(volumeMountPoint)
    if parent == "/" || parent == "." { break }
    volumeMountPoint = parent
}
dockerArgs = append(dockerArgs, "-v", opts.DockerVolume+":"+volumeMountPoint)
```

`/workdir/scratch/task_X` resolves to `/workdir`, so **the entire named volume is mounted read-write into every tool container** — including every other task's scratch directory and the staged outputs. `docker-compose.yml` ships exactly this configuration (`DOCKER_VOLUME=gowe-workdir` at line 121, `gowe-workdir:/workdir` at line 124, `-stage-out file:///workdir/outputs`).

Under this document's own threat model — untrusted tools, embargoed data — this is cross-task read **and write**: one task can read another's inputs/outputs and tamper with results before they are staged out.

**Fix:** mount only the task's own subtree (Docker `volume-subpath`, already used elsewhere in this function at `:313-315`), or use per-task volumes. Do not derive the mount point by walking to the volume root.

### F11 — Bind mounts are read-write except for declared inputs; worker holds the Docker socket · **High**

`CollectInputMounts` correctly marks declared inputs `readonly`/`ro`, but **IWDR mounts and `ExtraBinds` do not**: `execute.go:349`, `:387`, `:394` (Docker) and `:614`, `:619` (Apptainer). `ExtraBinds` is precisely the pre-staged reference-dataset path (`--pre-stage-dir`, `--dataset id=path`, `--extra-bind`), so a tool gets **write access to shared reference datasets** — corruptible for every subsequent task on that worker.

Separately: `docker-compose.yml:123` mounts `/var/run/docker.sock` into the **worker** container. The socket is correctly kept out of *tool* containers, but the worker itself is therefore root-equivalent on the host. §8's "never the Docker socket" guidance is at odds with what the repo ships.

**Fix:** default `ExtraBinds` and IWDR mounts to `ro` unless a task explicitly needs write; for untrusted tasks force `ro` unconditionally. Reconsider the socket mount in the worker service.

---

## 5. Layer 0 — harden the existing backends

Highest safety-per-line-changed. Gate the strict profile on the task's trust level so trusted workflows keep today's behaviour exactly.

### 5.1 Docker (`internal/toolexec/execute.go`)

```go
dockerArgs := []string{"run", "--rm", "-i"}

if task.Trust == model.TrustUntrusted {
    dockerArgs = append(dockerArgs,
        "--user", uidGid,                        // F2
        "--network", "none",                     // F1 · policy, ignores NetworkAccess
        "--cap-drop", "ALL",                     // F6
        "--security-opt", "no-new-privileges",
        "--security-opt", "seccomp="+seccompProfile,
        "--read-only",
        "--tmpfs", "/tmp:rw,nosuid,nodev,size="+tmpSize,
        "--pids-limit", "512",
    )
} else if !hasNetworkAccess(tool) {
    dockerArgs = append(dockerArgs, "--network", "none")  // existing behaviour
}

// F3: secrets via env-file, not argv
if len(opts.SecretEnvVars) > 0 {
    envFile, err := writeEnvFile(taskDir, opts.SecretEnvVars) // mode 0600, removed after
    if err != nil {
        return nil, fmt.Errorf("write env file: %w", err)
    }
    defer os.Remove(envFile)
    dockerArgs = append(dockerArgs, "--env-file", envFile)
}

e.logger.Debug("docker command", "args", redactEnvArgs(dockerArgs))  // F3
```

### 5.2 Apptainer (`internal/toolexec/execute.go`)

```go
apptainerArgs := []string{apptainerSubcmd,
    "--containall",   // F4 · no host env, no implicit binds
    "--cleanenv",     // F4
}

// F1: --net is silently ignored without unprivileged userns.
// For untrusted work that must be a hard failure, not a silent downgrade.
if task.Trust == model.TrustUntrusted {
    if !userNamespacesAvailable() {
        return nil, fmt.Errorf("untrusted task %s: apptainer network isolation unavailable "+
            "(unprivileged user namespaces required)", task.ID)
    }
    apptainerArgs = append(apptainerArgs, "--net", "--network", "none")
}

// F7: prefer a verified local SIF over live docker://
resolvedImage, err := resolveVerifiedImage(dockerImage, opts.ImageDir, opts.Keyring)
```

### 5.3 Cross-cutting

- **No secrets for untrusted tasks.** Treat `--secret` / `--secret-file` and per-task token delegation as trusted-only. `toolexec` should refuse to inject them when `trust=untrusted`.
- **Reject untrusted on unhardened paths** (F8): `executor/apptainer.go` and the legacy `_base_command` branch of `executor/docker.go` return an error for untrusted tasks.
- **`local` is untrusted-ineligible** (F5), enforced at checkout, not just at exec.

---

## 6. Layer 1 — a sandboxed executor backend

Hardened args cannot fix F9. Add a sixth backend to the existing registry — `k8s-sandboxed` — running each task as its **own ephemeral pod** under `runtimeClassName: kata` (microVM, own kernel) or `gvisor`. The worker stops executing binaries and becomes a **dispatcher**: create pod, stream status/logs, collect outputs, delete.

| Property | docker / apptainer (today) | k8s-sandboxed (proposed) |
|---|---|---|
| Where the tool runs | On / next to the worker host | Own Kata microVM / gVisor pod |
| Kernel | Shared with worker | Own kernel (Kata) |
| Lifetime | Runtime on a long-lived worker | One task = one pod, destroyed after |
| Docker daemon on node | Required (docker backend) | None |
| Worker's role | Executes the binary | Creates / watches a pod via K8s API |

This fits GoWe's existing `worker` backend concept — the remote unit simply becomes a sandboxed pod. It is the same pattern Toil, Cromwell, Argo and cwl-tes use for their Kubernetes backends. `toolexec` keeps building mounts/env/GPU config; it emits a **pod spec** instead of a local `exec`.

### 6.1 Registry integration

The `Executor` interface (`Type()`, `Submit()`, `Status()`, `Cancel()`, `Logs()`) is already the right shape — `Submit()` returns an `externalID`, which becomes the pod name, and `Status()` polls it. This maps cleanly onto the scheduler's existing async-polling phase (tick phase 4).

```go
Register(&KataSandboxExecutor{
    RuntimeClass: "kata",              // or "gvisor"
    Namespace:    "gowe-untrusted",
    Ephemeral:    true,                // one Pod per task, deleted on completion
})
```

### 6.2 Per-task pod

```yaml
apiVersion: v1
kind: Pod
metadata: { name: gowe-task-<id>, namespace: gowe-untrusted }
spec:
  runtimeClassName: kata               # own kernel · microVM boundary (F9)
  automountServiceAccountToken: false
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true                 # F2
    runAsUser: 1000
    seccompProfile: { type: RuntimeDefault }
  containers:
    - name: tool
      image: registry.internal/tool@sha256:...   # pinned digest (F7)
      command: [ ... ]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: { drop: [ "ALL" ] }        # F6
      resources:
        limits: { cpu: "4", memory: 16Gi }       # from CWL ResourceRequirement
      volumeMounts: [ { name: work, mountPath: /var/spool/cwl } ]
  volumes: [ { name: work, emptyDir: {} } ]      # ephemeral per-task scratch
# NetworkPolicy (Cilium): default-deny egress; allow only the GoWe API + scoped staging
```

---

## 7. Trust routing

Do this **before** Layer 1 — it is the plumbing everything else rides on.

**This is not greenfield — extend the existing mechanism, don't parallel it.** `SPECIFICATION.md` §13.2 already defines a "Trust boundary invariant", and ADR-0009 / ADR-0010 establish group-scoped least privilege. Most importantly, `scheduler.Config.TokenInjectGroups` (`internal/scheduler/loop.go:37-42`, wired at `cmd/server/main.go:358`) **already implements operator-designated trusted worker groups** and defaults to empty — i.e. the "only these groups receive credentials" concept exists and is server-assigned. `pkg/model` has no `Trust` field, so the task-level notion is genuinely new, but it should build on `--token-inject-groups` rather than introduce a competing axis.

Note also that ADR-0009 records that worker endpoints are **open when no worker keys are configured** — any trust model must account for that default.

- **Add a `trust` capability** (`trusted` | `untrusted`) to the task model and worker registration. Checkout matching then guarantees untrusted tasks only land on sandboxed, **credential-free** worker groups (`--group untrusted-sandbox`).
- **Gate secret and token injection** on `trust=trusted` in `toolexec`.
- **Broker real I/O.** If an untrusted tool must read/write data, a worker-controlled sidecar issues a **scoped, short-lived credential limited to that task's own inputs/outputs** in SHOCK/S3 — never the raw user token.
- **Scope and rotate `--worker-key` per group**, and least-privilege the work API so a compromised untrusted worker cannot enumerate other tasks' tokens or check out trusted work.

A natural expression in existing CWL hints:

```yaml
hints:
  gowe:Execution:
    executor: k8s-sandboxed
    worker_group: untrusted-sandbox
```

…but note **`trust` must be server-assigned, not hint-declared** — otherwise it repeats the F1 mistake of letting the untrusted document choose its own policy.

---

## 8. Deployment & blast radius (Kubernetes)

The runtime is necessary but not sufficient. Around the sandboxed backend:

| Zone | Runtime | Rationale |
|---|---|---|
| Edge — public API + UI | `gvisor` | Front line to hostile traffic; no sensitive data resident |
| Compute — analysis on uploads | `kata` | Untrusted input × memory-unsafe tools × embargoed data nearby |
| Backend — MG-RAST/ANL integration, stores, creds | `runc` | No internet ingress; reached only via internal services |

- **Cilium default-deny** NetworkPolicy with **FQDN egress allow-listing** to only the GoWe API and ANL/MG-RAST hosts; untrusted compute pods get zero internet egress.
- **Dedicated tainted node pool** for the Kata compute tier; never co-schedule untrusted execution with embargoed-data or control-plane workloads.
- **Embargoed data** on encrypted, access-scoped volumes mounted only by the specific task that needs them; no dataset affinity (`prestage`/`cache`) from untrusted worker groups to controlled datasets.
- **Pod Security Admission `restricted`**, plus **Kyverno** to enforce: pods touching controlled-data PVCs must be `runtimeClassName: kata`, zero-egress, resource-limited, non-root.
- **Image builds stay rootless/daemonless** (BuildKit rootless / Buildah / Kaniko) — never the Docker socket.

---

## 9. HPC / Apptainer caveat

`deploy/apptainer/` + SLURM cannot use Kata or gVisor on typical shared compute nodes. Apptainer can be hardened (`--containall`, `--cleanenv`, no `--fakeroot`, restricted binds, SLURM cgroup isolation) but that is **not** a strong boundary against hostile code — and F1 notes its network isolation degrades *silently* where unprivileged user namespaces are unavailable, which is common on HPC.

**Recommendation:** route untrusted workflows **exclusively** to the Kubernetes + Kata path. Keep the Apptainer/SLURM backend for trusted internal workflows only, and enforce that at checkout rather than by convention.

---

## 10. Phased plan

### Phase 0 — fix live defects *(now; independent of the trust roadmap)*
- **Redact secrets at both log sites** — `execute.go:427` and `:680` — reusing `redactSecrets()`. Closes the `SPECIFICATION.md` §13.5 violation (F3).
- **Scope the `DockerVolume` mount to the task subtree** (F10) — cross-task read/write exists today.
- Secrets off argv → `--env-file` (F3).
- Default `ExtraBinds` / IWDR mounts to `ro` (F11).

### Phase 0b — harden in place *(benefits trusted code too)*
- Add `--containall` / `--cleanenv` to Apptainer (F4).
- Add `--user`, `--cap-drop=ALL`, `no-new-privileges`, `--pids-limit` (F2, F6).
- Make silent-ignore of Apptainer `--net` an explicit warning (F1b).
- Validate `Directory` input `location` paths before `MkdirAll` (F8).
- **Add arg-vector assertions to the executor tests** — no test currently asserts `--network none` or any other flag, so every control in §3.2 is unguarded against regression.

### Phase 1 — plumb the rails *(everything still runs trusted)*
- Add server-assigned `trust` to task model, worker registration, checkout matching.
- Gate secret/token injection on `trust=trusted` (§5.3).
- Reject untrusted on `local` and on the legacy executor paths (F5, F8).

### Phase 2 — the real sandbox
- Implement the `k8s-sandboxed` (Kata) backend; keep existing backends untouched.
- Route the first untrusted workflows there with zero secrets and zero egress.
- Force `--network none` for untrusted regardless of `NetworkAccess` (F1).

### Phase 3 — defence in depth
- Scoped data broker; Cilium FQDN egress; pinned digests + image signing (F7).
- Kyverno enforcement; audit logging on controlled-data access.

---

## 11. Handoff

### For the GoWe engineering team

- [ ] **Treat F3 and F10 as bugs, not roadmap items** — both are live in the current trusted deployment, and F3 is a `SPECIFICATION.md` §13.5 violation.
- [ ] Confirm findings against current `HEAD` — this review read `main` @ `0c50df7`.
- [ ] Decide the `trust` model: extend `--token-inject-groups` rather than adding a parallel axis. **Must not be tool-declarable.**
- [ ] Land Phase 0 items — smallest first: redact both log sites, then `DockerVolume` subpath, then secrets off argv.
- [ ] Decide the fate of the legacy `_base_command` path (F8): route through `toolexec`, or reject untrusted there.
- [ ] Define the egress-broker contract for untrusted tasks needing staged I/O.
- [ ] Prototype `k8s-sandboxed` as a new registry backend.

### For Claude Code (starter prompt)

**Start here — the two live defects, no `trust` field needed:**

> In `internal/toolexec/execute.go`, secret values are leaking into logs, violating `SPECIFICATION.md` §13.5. Secrets are appended to argv at `:412-414` (Docker) and `:635-637` (Apptainer), then the full arg vectors are debug-logged at `:427` and `:680`. Redact both using the existing `redactSecrets()` helper from `internal/worker/worker.go:508`, and replace the argv injection with a `0600` `--env-file` written into the task dir and removed after. Separately, at `:294-302` the `DockerVolume` mount point is computed by walking `workDir` to the volume root, mounting the entire shared volume read-write into every tool container — scope it to the task's own subtree using the `volume-subpath` form already used at `:313-315`. Add table-driven tests asserting the resulting arg vectors; none currently exist.

**Then the trust work:**

> Add a server-assigned `Trust` field (`trusted` | `untrusted`) to `pkg/model` Task and to worker registration/checkout matching — extending the existing `TokenInjectGroups` mechanism (`internal/scheduler/loop.go:37-42`) rather than adding a parallel axis. It must **not** be settable from CWL hints. Then: (1) add `--containall --cleanenv` to the Apptainer args (~`:575`); (2) add an untrusted profile to the Docker args (~`:251`): `--user`, `--cap-drop=ALL`, `--security-opt no-new-privileges`, `--read-only` + tmpfs, `--pids-limit`, and force `--network none` ignoring **all four** spellings accepted by `requirements.HasNetworkAccess` (`requirements.go:16-47`, incl. the `hints:` forms); (3) skip `SecretEnvVars` entirely when `trust=untrusted`; (4) force `ro` on `ExtraBinds`/IWDR mounts. Keep the trusted path byte-identical. Make `internal/executor/apptainer.go` and the legacy `_base_command` branches of `docker.go`/`local.go` return an error for untrusted tasks.

---

## 12. Provenance

Findings verified by reading source on `main` @ `0c50df7` (v0.13.2):

- `internal/toolexec/execute.go` — primary execution path (local :60-102, Docker :250-429, Apptainer :566-682); `DockerVolume` mount walk :294-302; secret injection :412-414, :635-637; debug logs :427, :680
- `internal/toolexec/executor.go` — `Options.SecretEnvVars` (:127)
- `internal/toolexec/helpers.go` — `hasNetworkAccess` (:123-125) → `internal/requirements/requirements.go:16-47`
- `internal/executor/docker.go` — legacy path (:141-149), `HasTool()` fork (:104), cwltool delegation (:193-232), `MkdirAll` on input location (:130-133)
- `internal/executor/apptainer.go` — legacy path only, no cwltool fork (:95-102), `MkdirAll` (:85-88)
- `internal/executor/local.go` — `HasTool()` fork (:64)
- `internal/worker/worker.go` — `redactSecrets()` (:508)
- `cmd/worker/main.go` — `--secret` / `--secret-file` wiring (:73-76, :195-211)
- `internal/scheduler/loop.go` — `TokenInjectGroups` (:37-42) · `cmd/server/main.go:358`
- `docker-compose.yml` — `-debug` (:31, :63, :90, :117), `DOCKER_VOLUME` (:121), docker socket (:123), `gowe-workdir` (:124)
- `SPECIFICATION.md` §13.2, §13.5 — secret handling invariants; ADR-0009, ADR-0010 — group-scoped least privilege

**Review history.** Rev. 1 of this analysis incorrectly listed "no shared-workdir cross-task persistence" as verified-safe; a second verification pass against full source found the `DockerVolume` case and it is now **F10 (Critical)**. Rev. 1 also under-graded F3 as second-order rather than a specification violation, and missed F10, F11, the `hints:` bypass in F1, and the arbitrary-path creation in F8.

Runtime-isolation guidance (Kata, gVisor, Cilium, Kyverno, Pod Security Admission) reflects general container-security practice, **not** GoWe-specific documentation or testing. Severity ratings assume the untrusted-code threat model; several findings are acceptable under the current trusted deployment.

Advisory only — not a compliance or legal assessment.
