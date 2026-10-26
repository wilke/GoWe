# GoWe alpha production (BV-BRC Copilot)

GoWe server under pm2 with a fresh database holding the 30 workflows the dev
Copilot uses. Built from branch `bvbrc-alpha`.

| File | What |
|---|---|
| `ecosystem.config.js` | pm2 app (same flags as dev's `~/GoWeServer/start_gowe_alpha.sh`) |
| `cwl/*.cwl` | The CWL registered on dev, exported from its database on 2026-10-26 |
| `workflows.json` | File → workflow name. The Copilot finds workflows **by name**, so names must match dev exactly |
| `register_workflows.py` | Registers `cwl/` under those names (`gowe register` would name them after the file) |

## Setup

```bash
export GOWE_HOME=/path/to/gowe-alpha
mkdir -p $GOWE_HOME/{bin,logs,uploads,data} && chmod 700 $GOWE_HOME

# 1. build (Go >= 1.24)
git clone -b bvbrc-alpha git@github.com:wilke/GoWe.git && cd GoWe
GOFLAGS=-buildvcs=false go build -o $GOWE_HOME/bin/gowe-server ./cmd/server
$GOWE_HOME/bin/gowe-server -version

# 2. token-encryption key: 32 bytes as hex or base64 TEXT (raw bytes are refused).
#    Back it up; without it the stored tokens cannot be decrypted.
openssl rand -hex 32 > $GOWE_HOME/token.key && chmod 600 $GOWE_HOME/token.key

# 3. start under pm2 (first start creates gowe.db)
GOWE_HOME=$GOWE_HOME pm2 start deploy/bvbrc-alpha/ecosystem.config.js
pm2 save
pm2 startup            # run the sudo command it prints, so GoWe survives a reboot
pm2 install pm2-logrotate
curl -s http://localhost:12007/api/v1/health

# 4. register the 30 workflows (needs an admin's BV-BRC token)
GOWE_SERVER=http://localhost:12007 GOWE_TOKEN="$(cat ~/.patric_token)" \
  python3 deploy/bvbrc-alpha/register_workflows.py
```

Tested 2026-10-26 on a scratch server: all 30 registered, and every
`GET /api/v1/workflows/<name>/inputs` was identical to dev's.

## Afterwards

- Port 12007 only needs to be reachable from the alpha orchestrator and gateway hosts.
- The host must reach `https://user.patricbrc.org/public_key` (token verification); otherwise every call is 401.
- Point the alpha Copilot at this server: `bvbrc-agents/shared/models.py` `gowe_url` and the gateway's `config.json` `gowe_url`.
- Before replacing the binary: `pm2 stop gowe-server`, copy `gowe.db*` aside (startup migrates the schema), then `pm2 start gowe-server`.
- After changing a spec on dev, re-export it here and re-run `register_workflows.py` (it dedups unchanged content).
