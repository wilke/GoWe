# Production Deployment — coconut

GoWe production setup on `coconut`, an 8x NVIDIA H200 NVL workstation.

## Machine Specs

| Resource | Value |
|----------|-------|
| Hostname | `coconut` |
| CPUs | 384 cores |
| Memory | 1.5 TiB |
| GPUs | 8x NVIDIA H200 NVL (143 GB each) |
| OS | Ubuntu, kernel 6.8.0-94-generic |
| Container runtime | Apptainer 1.4.5 |
| Go | Not natively installed; compiled via `apptainer exec docker://golang:1.24` |

## Directory Layout

```
/scout/
├── Experiments/GoWe/          # Source code + binaries
│   ├── bin/                   # Compiled binaries (symlinked to versioned builds)
│   └── scripts/               # Start/stop/conformance scripts
├── containers/                # SIF container images
│   ├── folding_prod.sif       # Production protein folding tools
│   ├── folding_compare_prod.sif
│   ├── alphafold.sif
│   ├── boltz.sif
│   ├── chai.sif
│   ├── esmfold.sif
│   ├── hmmer.sif
│   ├── mmseqs2.sif
│   └── python.sif
└── wf/                        # Production runtime data
    ├── gowe/
    │   ├── gowe.db            # SQLite database (WAL mode)
    │   ├── logs/              # Server and worker logs
    │   ├── pids/              # PID files for start/stop scripts
    │   ├── uploads/           # File upload storage (local backend)
    │   ├── workdir/           # Per-worker working directories
    │   │   ├── cpu-worker-1/      # task dirs are deleted after SUCCESS (see --keep-task-dirs)
    │   │   ├── worker-1/
    │   │   └── ragstack-oa-1/
    │   ├── secrets.env        # HuggingFace tokens (mode 600)
    │   └── token.key          # Token-encryption key (--token-key-file, mode 600)
    └── data/                  # Staged output files (also the --redeliver-source-dirs allowlist)

/local_databases/              # Pre-staged reference datasets
├── alphafold/                 # AlphaFold model weights + databases
├── boltz/                     # Boltz model weights
└── chai/                      # Chai model weights
```

## Starting the Server

### Quick Start

```bash
cd /scout/Experiments/GoWe
./scripts/start-server.sh
```

This starts 1 server + 2 workers with default settings.

### Configuration

The start script reads environment variables (all have defaults):

| Variable | Default | Description |
|----------|---------|-------------|
| `GOWE_PORT` | `8091` | Server listen port |
| `BASE_DIR` | `/scout/wf` | Root for database, logs, uploads, workdirs |
| `IMAGE_DIR` | `/scout/containers` | SIF image directory |
| `PRE_STAGE_DIR` | `/local_databases` | Pre-staged reference data |
| `NUM_WORKERS` | `2` | Number of workers to start |
| `LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `ADMINS` | `awilke,awilke@bvbrc,olson,olson@bvbrc` | Admin usernames |

Example with overrides:

```bash
NUM_WORKERS=4 LOG_LEVEL=debug ./scripts/start-server.sh
```

### What the Start Script Does

1. Creates required directories under `$BASE_DIR/gowe/`
2. Starts `gowe-server` on port `$GOWE_PORT` with:
   - `--default-executor worker` (routes all work through workers)
   - `--allow-anonymous` with executors `local,docker,worker,container`
   - `--scheduler-poll 100ms`
   - `--upload-backend local` with uploads in `$BASE_DIR/gowe/uploads`
   - `--workspace-staging server` (server-side ws:// staging)
3. Waits for health check to pass
4. Starts `$NUM_WORKERS` workers, each with:
   - `--runtime apptainer`
   - `--gpu --gpu-id $i` (GPU IDs start at 1; GPU 0 is reserved)
   - `--image-dir /scout/containers`
   - `--pre-stage-dir /local_databases`
   - `--workspace-stager` enabled
   - `--stage-out file:///scout/wf/data`
   - `--poll 500ms`
5. Writes PIDs to `$BASE_DIR/gowe/pids/`
6. Logs to `$BASE_DIR/gowe/logs/`

## Stopping the Server

```bash
./scripts/stop-server.sh
```

Sends SIGTERM to workers first (lets them finish current tasks), waits 2 seconds for deregistration, then stops the server. Force-kills after 10 seconds if processes don't exit.

## Health Check

```bash
curl -s http://localhost:8091/api/v1/health | python3 -m json.tool
```

Returns executor availability, worker summary (online/offline counts, runtimes, groups), and uptime.

## Metrics

`gowe-server` can expose Prometheus metrics on a **second, unauthenticated HTTP listener** — separate from the main API/UI server, with no auth middleware, no request logging, and no route other than `/metrics`. It is off by default.

```bash
gowe-server ... --metrics-addr localhost:9090
```

| Flag | Default | Description |
|------|---------|-------------|
| `--metrics-addr` | `""` | Listen address for the metrics-only server. Empty disables metrics entirely (no registry is even constructed, so instrumentation is a true no-op). |
| `--metrics-workflow-label` | `true` | Include the workflow name as a label. Set `false` on a shared/multi-tenant server if workflow names themselves are sensitive — every observation collapses to `workflow="_all"`. |
| `--metrics-label-cap` | `200` | Cap on distinct values tracked per user-authored label (`workflow`, `step`); values beyond the cap collapse into `_other` so an unbounded set of workflow/step names can never blow up cardinality on a shared server. |

**Bind guidance:** because this endpoint has no authentication of its own, bind `--metrics-addr` to `localhost` or a private/internal interface — never to a publicly routable address — and let your Prometheus server (or a reverse proxy in front of it) reach it over a private network, an SSH tunnel, or a scrape-side sidecar. Do **not** put `--metrics-addr` on the same address as `--addr`; they are intentionally two separate `http.Server`s so a metrics scrape can never touch the authenticated API surface (and vice versa).

**Prometheus scrape config example:**

```yaml
scrape_configs:
  - job_name: gowe-server
    static_configs:
      - targets: ["localhost:9090"]
```

Metrics cover task queue/run/staging durations, submission wall time, per-tick scheduler phase durations, retry/failure/skip counters, and live gauges for task/submission/worker counts and per-group queue depth. See `SPECIFICATION.md` §12 for the full metric catalog.

## TLS & Secure Cookies

The server transmits provider tokens (BV-BRC) and session cookies, so production traffic **must** be encrypted. There are two supported deployment modes.

Session-cookie hardening is tied to the transport: the `Secure` attribute (which stops browsers from ever sending the cookie over plain HTTP) is set automatically when the server knows the connection is HTTPS. The relevant flags:

| Flag | Effect |
|------|--------|
| `--tls-cert <path>` / `--tls-key <path>` | Terminate TLS in-process (native HTTPS). Both required together. Implies `--secure-cookies`. |
| `--secure-cookies` | Always mark session cookies `Secure`. Use when a trusted upstream proxy always terminates TLS. |
| `--behind-proxy` | Trust the `X-Forwarded-Proto` header to mark cookies `Secure` per-request. Enable **only** behind a trusted reverse proxy — a direct client can otherwise spoof the header. |

### Mode 1 — Native TLS (server terminates HTTPS)

The server terminates TLS itself. No external proxy is required.

```bash
gowe-server \
  --addr :8443 \
  --tls-cert /etc/gowe/tls/fullchain.pem \
  --tls-key  /etc/gowe/tls/privkey.pem \
  ...
```

- The server calls `ListenAndServeTLS`; only HTTPS is accepted on `--addr`.
- `--secure-cookies` is implied automatically, so session cookies are marked `Secure`.
- If only one of `--tls-cert` / `--tls-key` is supplied, the server refuses to start.

Use a real certificate in production (e.g. Let's Encrypt / your org CA). Self-signed certs are fine for internal testing but browsers and API clients will warn.

### Mode 2 — External TLS terminator (reverse proxy)

The server serves plain HTTP on a loopback/private interface and a proxy (nginx, Caddy, an ingress controller, a load balancer) terminates TLS and forwards requests. The proxy **must** set `X-Forwarded-Proto: https` on forwarded requests.

```bash
gowe-server \
  --addr 127.0.0.1:8091 \
  --behind-proxy \
  ...
```

With `--behind-proxy`, cookies are marked `Secure` for any request the proxy tags as HTTPS via `X-Forwarded-Proto`. If your proxy always terminates TLS for every client, you can instead use `--secure-cookies` to force `Secure` unconditionally.

Example nginx snippet:

```nginx
location / {
    proxy_pass http://127.0.0.1:8091;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;  # required for Secure cookies
}
```

> **Warning:** Only enable `--behind-proxy` when a proxy you control actually sits in front of the server and overwrites `X-Forwarded-Proto`. If the server is directly reachable, a client can send a forged `X-Forwarded-Proto: https` header. Do not combine `--behind-proxy` with a directly-exposed listener.

Running with `--secure-cookies` but neither native TLS nor `--behind-proxy` logs a warning: browsers will refuse to send `Secure` cookies over plain HTTP, breaking login.

## Secrets

Worker secrets live in `/scout/wf/gowe/secrets.env` (mode `600`, never committed):

```
# HuggingFace Hub authentication
HUGGING_FACE_HUB_TOKEN=<token>
HF_TOKEN=<token>
```

Workers load these via `--secret-file`. Secret values are injected into containers at runtime and never sent to the server, stored in task data, or exposed in API responses or logs.

### Provider-token encryption at rest

The server encrypts each submitter's BV-BRC/MG-RAST token before persisting it (in
`submissions.user_token` and any bearer credential inside `tasks.runtime_hints`), using
AES-256-GCM under a server-held key. Configure the key with **one** of:

- `GOWE_TOKEN_KEY` — a 32-byte key, base64 or hex encoded, in the server's environment.
- `--token-key-file <path>` — a file (mode `600`) whose contents are the encoded key; takes
  precedence over the env var.

Generate a key:

```bash
openssl rand -base64 32   # 32-byte AES-256 key
```

Behavior:

- **Key set** → tokens are encrypted at rest; any legacy plaintext rows are re-encrypted on
  startup. Decrypted values live only in memory on the delegated-execution path and are never
  logged.
- **No key** → the server **fails closed**: submissions that carry a delegated provider token
  are rejected at persistence time. Pass `--allow-plaintext-tokens` only for local/dev or a
  staged migration; it stores tokens unencrypted (a startup warning is logged).

Rotating the key requires decrypting with the old key and re-encrypting with the new one;
there is no in-place multi-key support yet, so rotate during a maintenance window.

## GPU Assignment

GPU 0 is reserved for interactive/other use. The start script assigns workers to GPUs starting at index 1:

| Worker | GPU ID | Device |
|--------|--------|--------|
| worker-1 | 1 | H200 NVL |
| worker-2 | 2 | H200 NVL |
| worker-3 | 3 | H200 NVL |
| ... | ... | ... |

Up to 7 GPU workers can run simultaneously (GPUs 1-7).

## Deploying a Release

Production runs **tagged releases**, not local builds. Each `vX.Y.Z` tag is built by
GoReleaser into per-binary archives named `<binary>_<X.Y.Z>_linux_amd64.tar.gz` (plus
`checksums.txt`) on the [GitHub release](https://github.com/wilke/GoWe/releases). Binaries
live in `bin/` as `<name>-vX.Y.Z`; the unversioned names (`gowe`, `gowe-server`,
`gowe-worker`, `cwl-runner`) are symlinks, so rolling back is repointing four links.

```bash
cd /scout/Experiments/GoWe
VER=0.15.0

# 1. Download the linux_amd64 archives and install them as versioned binaries
mkdir -p /tmp/gowe-release-$VER && cd /tmp/gowe-release-$VER
gh release download v$VER --repo wilke/GoWe --pattern '*_linux_amd64.tar.gz' --pattern checksums.txt
sha256sum -c --ignore-missing checksums.txt
for b in gowe gowe-server gowe-worker cwl-runner; do
  tar -xzf "${b}_${VER}_linux_amd64.tar.gz" "$b"
  install -m 755 "$b" /scout/Experiments/GoWe/bin/${b}-v$VER
done

# 2. Repoint the symlinks
cd /scout/Experiments/GoWe/bin
for b in gowe gowe-server gowe-worker cwl-runner; do ln -sfn "${b}-v$VER" "$b"; done
ls -l gowe gowe-server gowe-worker cwl-runner

# 3. Restart the server first, then the workers
cd /scout/Experiments/GoWe
SERVER_PID=$(pgrep -f "gowe-server --addr :8091")
tr '\0' ' ' < /proc/$SERVER_PID/cmdline > /tmp/server_cmdline   # keep the exact flags
kill -TERM $SERVER_PID && sleep 3
nohup $(cat /tmp/server_cmdline) > /scout/wf/gowe/logs/server-v$VER.log 2>&1 &
until curl -sf http://localhost:8091/api/v1/health > /dev/null; do sleep 1; done
# then SIGTERM each gowe-worker (it finishes its current task) and relaunch it with its
# previous cmdline — capture it from /proc the same way; per-worker flags are in docs/STATUS.md

# 4. Verify: every worker registers with the new version
curl -s http://localhost:8091/api/v1/workers | python3 -c \
  "import sys,json; [print(w['name'], w['state'], w.get('version')) for w in json.load(sys.stdin)['data']]"
```

Restart the server before the workers: the server owns the schema migrations and the API
surface. A restarted worker re-registers on start; any task it was running is requeued by
the server (it no longer appears in the worker's heartbeat) and re-dispatched from scratch.

### Server flags added in 0.15.0

| Flag | Purpose |
|------|---------|
| `--redeliver-source-dirs <dir>[,<dir>]` | Allowlist of local directories the admin re-delivery endpoint may read staged originals from — the shared stage-out directory (`/scout/wf/data` here). Without it `redeliver` refuses every local re-upload. The live server passes it; `scripts/start-server.sh` does not yet. |
| `--workspace-url` | Now also configures the web UI's workspace browser/uploads, which run under the logged-in user's session token only (no server service-account fallback). |
| `--upload-max-size` | Now also caps web UI workspace uploads (`413` when exceeded; default 1 GB). |

### Recovering Workspace outputs delivered before 0.15.0

Every binary output post-staged to a BV-BRC Workspace by a pre-0.15.0 server is corrupt in
the stored copy (issue #172). After deploying 0.15.0 with `--redeliver-source-dirs
/scout/wf/data`, run `gowe admin verify-outputs --all --output-state delivered,upload_failed`,
then `gowe admin redeliver <id> --dry-run` and `gowe admin redeliver <id>` per affected
submission. Procedure, requirements, and limits:
[`upgrading.md`](upgrading.md#admin-recovery-of-workspace-outputs-180).

## Building Binaries (development builds)

Go is not installed natively. All builds run through Apptainer. Dev builds are for testing
against a scratch database; production is deployed from releases (above).

```bash
# Build all binaries with version tags
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "make dev"

# Update symlinks to the new build
DEV_TAG=$(ls -t bin/gowe-server-* | head -1 | sed 's|bin/gowe-server-||')
cd bin
ln -sf gowe-server-$DEV_TAG gowe-server
ln -sf gowe-$DEV_TAG gowe
ln -sf gowe-worker-$DEV_TAG gowe-worker
ln -sf cwl-runner-$DEV_TAG cwl-runner
```

Or use the `make build` target which produces unversioned binaries directly:

```bash
mkdir -p /tmp/gomod && apptainer exec --bind /tmp/gomod:/go docker://golang:1.24 bash -c "make build"
```

## Log Locations

| Log | Path |
|-----|------|
| Server | `/scout/wf/gowe/logs/server.log` |
| Worker N | `/scout/wf/gowe/logs/worker-N.log` |

For debug-level logging, set `LOG_LEVEL=debug` before starting.

## Database

SQLite in WAL mode at `/scout/wf/gowe/gowe.db`. Single-writer, no external database server needed. Back up by copying the file while the server is stopped, or use `.backup` via the SQLite CLI.

## Troubleshooting

**Server won't start — port already in use:**
```bash
pgrep -af 'gowe-server'       # Find existing processes
./scripts/stop-server.sh       # Graceful stop
```

**Worker fails to register:**
Check the worker log for connectivity issues:
```bash
tail -50 /scout/wf/gowe/logs/worker-1.log
```

**Container image not found:**
Verify the SIF image exists in `/scout/containers/` and the `--image-dir` flag is set.

**GPU not available to worker:**
```bash
nvidia-smi                     # Verify GPU visibility
apptainer exec --nv docker://nvidia/cuda:12.0-base nvidia-smi  # Test inside container
```

**Database locked:**
GoWe uses `max_open_conns=1` with WAL mode. If the database reports "locked", ensure only one server instance is running against the same `.db` file.
