# Running GoWe alpha under pm2

Host `elm`, account `svcbvbrc`, home `/disks/p3/GoWeServer`. Two pm2 apps,
both defined in `ecosystem.config.js`:

| App | What | Runs |
|---|---|---|
| `gowe-server` | The GoWe server on `127.0.0.1:7000` (elm only) | Always; pm2 restarts it if it exits (5 s apart, gives up after 10 failed starts in a row) |
| `gowe-backup` | `backup_db.py` into `/vol/bvbrc/db-backups/gowe` | Once when started, then daily at 02:30 |

pm2 reads `ecosystem.config.js` only when an app is **started**. `pm2 restart`
reuses the arguments from the first start, so after changing the config file,
`pm2 delete <app>` and start it again (see "Changing the config").

## First start

Run from the repo checkout so the relative config path works:

```bash
cd /disks/p3/GoWeServer/GoWe

# 1. the server first: its first start creates gowe.db
pm2 start deploy/bvbrc-alpha/ecosystem.config.js --only gowe-server
sleep 5
pm2 status                                     # gowe-server: online
curl -s http://localhost:7000/api/v1/health    # "status":"healthy", your version
ss -tlnp | grep ':7000 '                       # must show 127.0.0.1:7000

# 2. then the backup job (starting it runs one backup right away)
pm2 start deploy/bvbrc-alpha/ecosystem.config.js --only gowe-backup
sleep 3
pm2 logs gowe-backup --lines 5 --nostream      # "... ok: N workflows, N submissions"
ls -la /vol/bvbrc/db-backups/gowe

# 3. log rotation (once per account)
pm2 install pm2-logrotate
```

If `gowe-server` shows `errored` or its restart count keeps climbing:
`pm2 logs gowe-server --lines 50 --nostream`. Common causes: port 7000 in use
(`ss -tlnp | grep ':7000 '`), `token.key` missing or not 64 hex characters,
`bin/gowe-server` missing.

## Starting at boot

pm2 itself has to be started by the system at boot; it then restores the apps
from its saved list.

```bash
# as svcbvbrc: save the current app list (~svcbvbrc/.pm2/dump.pm2)
pm2 save

# as svcbvbrc: print the boot command
pm2 startup systemd
```

`pm2 startup` prints one line starting with `sudo env PATH=...`. Someone with
sudo runs **that exact line** once. It installs and enables the systemd unit
`pm2-svcbvbrc.service`, which runs `pm2 resurrect` as `svcbvbrc` at boot.

Check it:

```bash
systemctl is-enabled pm2-svcbvbrc     # enabled
systemctl status pm2-svcbvbrc         # active
```

Test it without rebooting the machine:

```bash
pm2 kill            # stops pm2 and both apps
pm2 resurrect       # what the boot unit does
pm2 status          # both apps back; gowe-backup ran once
curl -s http://localhost:7000/api/v1/health
```

**Run `pm2 save` again whenever the app list changes** (an app added, deleted
or re-created). The boot unit restores the last saved list, not the current one.

If pm2 itself is upgraded (`npm install -g pm2`), run `pm2 update`, then
`pm2 unstartup systemd` and `pm2 startup systemd` again (with sudo), because the
unit file records the path to the pm2 binary.

## Day to day

| Task | Command |
|---|---|
| Status | `pm2 status` |
| Server log | `pm2 logs gowe-server` (files: `/disks/p3/GoWeServer/logs/gowe-server.{out,err}.log`) |
| Backup log | `pm2 logs gowe-backup --nostream` |
| Health | `curl -s http://localhost:7000/api/v1/health` |
| Stop / start | `pm2 stop gowe-server` / `pm2 start gowe-server` |
| Take a backup now | `pm2 restart gowe-backup` |

`pm2 stop` sends SIGINT; GoWe shuts down cleanly and pm2 kills it only after
15 s.

## Updating GoWe

```bash
cd /disks/p3/GoWeServer/GoWe
git pull
export PATH=/disks/p3/GoWeServer/go-1.26.4/bin:$PATH \
       GOPATH=/disks/p3/GoWeServer/gopath GOTOOLCHAIN=local
V=$(git describe --tags --always --dirty)
GOFLAGS=-buildvcs=false go build -ldflags "-X main.Version=$V" \
  -o /disks/p3/GoWeServer/bin/gowe-server.new ./cmd/server

pm2 stop gowe-server
cp /disks/p3/GoWeServer/gowe.db /disks/p3/GoWeServer/gowe.db.pre-$V   # startup may migrate the schema
mv /disks/p3/GoWeServer/bin/gowe-server.new /disks/p3/GoWeServer/bin/gowe-server
pm2 start gowe-server
curl -s http://localhost:7000/api/v1/health    # new version
```

Building to `gowe-server.new` first keeps the running binary untouched if the
build fails. To roll back: stop, put the old binary and the `gowe.db.pre-*`
copy back (remove `gowe.db-wal` / `gowe.db-shm`), start.

## Changing the config

After editing `ecosystem.config.js` (port, flags, backup folder):

```bash
cd /disks/p3/GoWeServer/GoWe
pm2 delete gowe-server
pm2 start deploy/bvbrc-alpha/ecosystem.config.js --only gowe-server
pm2 save
```

Same for `gowe-backup`. `pm2 restart` would keep the old arguments.
