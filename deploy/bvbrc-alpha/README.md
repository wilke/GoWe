# GoWe alpha production (BV-BRC Copilot)

GoWe server under pm2 with a fresh database holding the 30 workflows the dev
Copilot uses. Built from branch `bvbrc-alpha`.

| File | What |
|---|---|
| `ecosystem.config.js` | pm2 apps `gowe-server` (same flags as dev's `~/GoWeServer/start_gowe_alpha.sh`, but `127.0.0.1:7000`) and `gowe-backup` |
| `PM2.md` | Running it under pm2: first start, starting at boot, day to day, updates |
| `cwl/*.cwl` | The CWL registered on dev, exported from its database on 2026-10-26 |
| `workflows.json` | File → workflow name. The Copilot finds workflows **by name**, so names must match dev exactly |
| `backup_db.py` | Online, integrity-checked, pruned database backup (see Backups) |
| `register_workflows.py` | Registers `cwl/` under those names (`gowe register` would name them after the file) |

## Setup

How to run and operate it under pm2 (first start, starting at boot, updates):
**[PM2.md](PM2.md)**.

```bash
export GOWE_HOME=/disks/p3/GoWeServer
mkdir -p $GOWE_HOME/{bin,logs,uploads,data} && chmod 700 $GOWE_HOME
cd $GOWE_HOME && git clone -b bvbrc-alpha git@github.com:wilke/GoWe.git

# 1. Go >= 1.24, unpacked in $GOWE_HOME (elm's system Go is 1.18)
curl -LO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz   # check sha256 against go.dev/dl
mkdir go-1.26.4 && tar -C go-1.26.4 --strip-components=1 -xzf go1.26.4.linux-amd64.tar.gz
export PATH=$GOWE_HOME/go-1.26.4/bin:$PATH GOPATH=$GOWE_HOME/gopath GOTOOLCHAIN=local
go version                                                # go1.26.4

# 2. build, stamped with the version
cd $GOWE_HOME/GoWe
V=$(git describe --tags --always --dirty)
GOFLAGS=-buildvcs=false go build -ldflags "-X main.Version=$V" -o $GOWE_HOME/bin/gowe-server ./cmd/server
$GOWE_HOME/bin/gowe-server -version

# 3. token-encryption key: 32 bytes as hex or base64 TEXT (raw bytes are refused).
#    Keep one copy elsewhere (not with the backups).
openssl rand -hex 32 > $GOWE_HOME/token.key && chmod 600 $GOWE_HOME/token.key

# 4. start under pm2: see PM2.md "First start" and "Starting at boot"

# 5. register the 30 workflows (needs an admin's BV-BRC token)
GOWE_SERVER=http://localhost:7000 GOWE_TOKEN="$(cat ~/.patric_token)" \
  python3 deploy/bvbrc-alpha/register_workflows.py
```

`gowe.db` must be on a local filesystem (`df -T` shows ext4/xfs, not nfs):
SQLite's WAL mode is not safe on network filesystems.

Tested 2026-10-26 on a scratch server: all 30 registered, and every
`GET /api/v1/workflows/<name>/inputs` was identical to dev's.

## Backups

`backup_db.py` copies `gowe.db` with SQLite's online backup API (safe while the
server runs; a plain `cp` of a WAL-mode database can be corrupt), runs
`PRAGMA integrity_check`, gzips it (mode 600) and keeps the newest 14.
`ecosystem.config.js` runs it daily at 02:30 as the pm2 app `gowe-backup`
into `/vol/bvbrc/db-backups/gowe` (it expects this repo cloned at
`$GOWE_HOME/GoWe`; `GOWE_BACKUP_DIR` overrides the destination). It runs once immediately when pm2 starts it.

- The backup folder must be writable by the account pm2 runs as, and should be
  on a different disk than `gowe.db`.
- Back up `token.key` **once, separately** (not next to the backups). A backup
  without it restores the job history but not the stored tokens.
- Check: `pm2 logs gowe-backup` — each run prints workflows / submissions / size;
  a failed run exits non-zero.
- Restore: `pm2 stop gowe-server`, move `gowe.db`, `gowe.db-wal`, `gowe.db-shm`
  aside, `gunzip -c backups/gowe-<stamp>.db.gz > gowe.db`, `pm2 start gowe-server`.
- Without pm2: `30 2 * * * python3 $GOWE_HOME/GoWe/deploy/bvbrc-alpha/backup_db.py --db $GOWE_HOME/gowe.db --dest <dir>` in crontab.

## Afterwards

- GoWe listens on `127.0.0.1:7000`, reachable only from elm. The alpha gateway
  (`workflow_monitor.gowe_url`) and orchestrator (`gowe_url`) run on elm and use
  `http://localhost:7000`. Nothing outside elm connects to GoWe.
- The host must reach `https://user.patricbrc.org/public_key` (token verification); otherwise every call is 401.
- Updating the binary: PM2.md "Updating GoWe".
- After changing a spec on dev, re-export it here and re-run `register_workflows.py` (it dedups unchanged content).
