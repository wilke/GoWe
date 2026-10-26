#!/usr/bin/env python3
"""Back up the GoWe SQLite database while the server is running.

Uses SQLite's online backup API, which is safe against a live WAL-mode
database (a plain cp of gowe.db can miss the -wal file and be corrupt).
Each backup is integrity-checked, gzipped, and old ones are pruned.

    backup_db.py --db $GOWE_HOME/gowe.db --dest $GOWE_HOME/backups [--keep 14]

Exit status is non-zero on any failure, so cron mail / pm2 logs show it.
"""
import argparse, gzip, os, shutil, sqlite3, sys, time
from pathlib import Path

p = argparse.ArgumentParser()
p.add_argument("--db", required=True)
p.add_argument("--dest", required=True)
p.add_argument("--keep", type=int, default=14, help="number of backups to keep")
a = p.parse_args()

dest = Path(a.dest)
dest.mkdir(parents=True, exist_ok=True)
stamp = time.strftime("%Y%m%d-%H%M%S")
tmp = dest / f".gowe-{stamp}.db"
out = dest / f"gowe-{stamp}.db.gz"

try:
    src = sqlite3.connect(f"file:{a.db}?mode=ro", uri=True, timeout=30)
    dst = sqlite3.connect(tmp)
    src.backup(dst, pages=1024, sleep=0.05)   # copies in steps; the server keeps writing
    src.close()
    ok = dst.execute("PRAGMA integrity_check").fetchone()[0]
    subs = dst.execute("SELECT count(*) FROM submissions").fetchone()[0]
    wfs = dst.execute("SELECT count(*) FROM workflows").fetchone()[0]
    dst.close()
    if ok != "ok":
        raise RuntimeError(f"integrity_check: {ok}")
    with open(tmp, "rb") as f, gzip.open(out, "wb", compresslevel=6) as g:
        shutil.copyfileobj(f, g)
    os.chmod(out, 0o600)   # holds encrypted user tokens and job history
finally:
    tmp.unlink(missing_ok=True)

old = sorted(dest.glob("gowe-*.db.gz"))[:-a.keep] if a.keep > 0 else []
for f in old:
    f.unlink()
print(f"{out} ok: {wfs} workflows, {subs} submissions, "
      f"{out.stat().st_size // 1024} KB; pruned {len(old)}")
