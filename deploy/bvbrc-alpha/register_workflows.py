#!/usr/bin/env python3
"""Register the BV-BRC workflows in cwl/ on a GoWe server under their exact names.

`gowe register` names a workflow after its file (Bacterial_Genome_Tree), but the
Copilot looks workflows up by the names dev uses (Bacterial Genome Tree), so this
posts each file with the name from workflows.json.

    GOWE_SERVER=http://localhost:7000 GOWE_TOKEN="$(cat ~/.patric_token)" ./register_workflows.py
"""
import json, os, sys, urllib.request
from pathlib import Path

here = Path(__file__).resolve().parent
server = os.environ.get("GOWE_SERVER", "http://localhost:7000").rstrip("/")
token = os.environ.get("GOWE_TOKEN", "").strip()
if not token:
    sys.exit("set GOWE_TOKEN to an admin's BV-BRC token")

failed = 0
for fn, name in json.loads((here / "workflows.json").read_text()).items():
    body = json.dumps({"name": name, "cwl": (here / "cwl" / fn).read_text()}).encode()
    req = urllib.request.Request(f"{server}/api/v1/workflows/", data=body, method="POST",
                                 headers={"Content-Type": "application/json",
                                          "Authorization": f"Bearer {token}"})
    try:
        with urllib.request.urlopen(req) as r:
            d = json.load(r)["data"]
        print(f"ok   {name:40} {d['id']}{'  (existing)' if d.get('deduplicated') else ''}")
    except urllib.error.HTTPError as e:
        failed += 1
        print(f"FAIL {name:40} HTTP {e.code}: {e.read().decode()[:300]}")
sys.exit(1 if failed else 0)
