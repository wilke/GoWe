#!/usr/bin/env python3
"""Diff the registered CWL specs against BV-BRC's authoritative app specs.

The app spec JSONs in Copilot/all_app_specs (mirrored from
dev_container/modules/*/app_specs) are the contract the AppService enforces in
preflight. A mismatch there surfaces only as an async Perl failure 20 minutes
after submission, so checking it mechanically is much cheaper than discovering
it one failed job at a time.

Focuses on group/record parameters, because those are the ones that produce
"Can't use string as a HASH ref".

Requires the GoWe server on :12007 (it reads GoWe's own parsed view of each
workflow's inputs, which is what the agent actually sees). Field-level
comparison needs the array-of-records parser fix, or every record input
reports 0 fields.

Usage: python3 scripts/diff_app_specs.py
"""
import json, os, re, urllib.request, glob

APPS = "/home/ac.cucinell/bvbrc-dev/Copilot/all_app_specs"
CWLD = "/home/ac.cucinell/bvbrc-dev/Copilot/Agents/GoWe/cwl/bvbrc-registered"
ALIAS = {"Homology": "HomologyService", "GenomeAssembly": "GenomeAssembly2"}

# GoWe's own parsed view of each workflow's inputs (authoritative, post-parser).
wfs = json.load(urllib.request.urlopen("http://localhost:12007/api/v1/workflows?limit=100"))
rows = wfs.get("data") or wfs.get("workflows") or []
gowe = {}
for w in rows:
    try:
        d = json.load(urllib.request.urlopen(
            f"http://localhost:12007/api/v1/workflows/{w['id']}/inputs"))
        gowe[w["name"]] = {i["id"]: i for i in (d.get("data") or d.get("inputs") or [])}
    except Exception as e:
        gowe[w["name"]] = {}

# CWL file -> app id, and the workflow name GoWe registered it under.
cwl_app = {}
for f in glob.glob(os.path.join(CWLD, "*.cwl")):
    txt = open(f, encoding="utf-8", errors="replace").read()
    m = re.search(r"bvbrc_app_id:\s*(\S+)", txt)
    n = re.search(r"^\s*label:\s*(.+)$", txt, re.M)
    if m:
        cwl_app[os.path.basename(f)] = (m.group(1), (n.group(1).strip().strip('"') if n else None))

print(f"{'app_id':<30} {'group/record params':<34} verdict")
print("-" * 92)
problems = []
for fname, (app_id, label) in sorted(cwl_app.items(), key=lambda kv: kv[1][0]):
    spec_name = ALIAS.get(app_id, app_id)
    path = os.path.join(APPS, spec_name + ".json")
    if not os.path.exists(path):
        print(f"{app_id:<30} {'-':<34} NO APP SPEC in all_app_specs")
        problems.append((app_id, "no app spec to check against"))
        continue
    raw = open(path, encoding="utf-8", errors="replace").read()
    try:
        spec = json.loads(raw)
    except json.JSONDecodeError:
        # BV-BRC app specs are read by a lenient Perl JSON loader and some
        # carry trailing commas. Strip them and retry.
        cleaned = re.sub(r",(\s*[}\]])", r"\1", raw)
        try:
            spec = json.loads(cleaned)
        except json.JSONDecodeError as e:
            print(f"{app_id:<30} {'-':<34} APP SPEC NOT VALID JSON ({e})")
            problems.append((app_id, f"app spec unparseable: {e}"))
            continue
    groups = [p for p in spec.get("parameters", [])
              if p.get("type") == "group" or (isinstance(p.get("type"), list) and p.get("allow_multiple"))]
    if not groups:
        print(f"{app_id:<30} {'(none)':<34} ok - no record params")
        continue
    # Find GoWe's parsed input for each group param.
    wf_inputs = None
    for nm, inputs in gowe.items():
        if any(g["id"] in inputs for g in groups):
            wf_inputs = inputs; break
    for g in groups:
        gid = g["id"]
        app_fields = sorted(f["id"] for f in (g.get("group") or []))
        if wf_inputs is None or gid not in wf_inputs:
            print(f"{app_id:<30} {gid:<34} MISSING from the registered CWL")
            problems.append((app_id, f"{gid}: not in CWL"))
            continue
        gi = wf_inputs[gid]
        t = gi.get("type", "")
        cwl_fields = sorted(f["name"] for f in (gi.get("fields") or []))
        is_rec = "record" in t
        if not is_rec:
            print(f"{app_id:<30} {gid:<34} NOT a record in CWL (type={t})")
            problems.append((app_id, f"{gid}: CWL type {t}, app wants a record"))
        elif app_fields and cwl_fields and app_fields != cwl_fields:
            print(f"{app_id:<30} {gid:<34} FIELD MISMATCH")
            print(f"{'':<30} {'':<34}   app: {app_fields}")
            print(f"{'':<30} {'':<34}   cwl: {cwl_fields}")
            problems.append((app_id, f"{gid}: fields differ"))
        else:
            print(f"{app_id:<30} {gid:<34} ok ({t}, {len(cwl_fields)} fields)")

print()
print(f"=== {len(problems)} problem(s) ===")
for a, p in problems:
    print(f"  {a}: {p}")
