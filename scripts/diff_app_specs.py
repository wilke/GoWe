#!/usr/bin/env python3
"""Diff REGISTERED GoWe workflows against BV-BRC's authoritative app specs.

The app spec JSONs in Copilot/all_app_specs (mirrored from
dev_container/modules/*/app_specs) are the contract the AppService enforces in
preflight. A mismatch there surfaces only as an async Perl failure long after
submission, so checking it mechanically is much cheaper than discovering it one
failed job at a time.

Focuses on group/record parameters, because those are what produce
"Can't use string as a HASH ref".

Each registered workflow is keyed to its app spec by the bvbrc_app_id hint in
its OWN raw CWL, read from the GoWe database. An earlier version matched by
input name instead -- it took the first workflow that happened to contain
`paired_end_libs` and compared every app spec against that one, producing a
long list of mismatches that were entirely artefacts. Do not reintroduce
name-based matching.

Needs the GoWe server on :12007 (for its parsed view of each workflow's
inputs, which is what the agent actually sees) and read access to gowe.db.
Field-level comparison also needs the array-of-records parser fix (GoWe
2987012), or every record input reports zero fields.

Usage: python3 scripts/diff_app_specs.py
"""

import json
import os
import re
import sqlite3
import sys
import urllib.request

APPS = "/home/ac.cucinell/bvbrc-dev/Copilot/all_app_specs"
DB = "/home/ac.cucinell/GoWeServer/gowe.db"
GOWE = "http://localhost:12007"

# app_id as written in the CWL hint -> app spec filename stem, where they differ.
ALIAS = {"Homology": "HomologyService", "GenomeAssembly": "GenomeAssembly2"}


def load_app_spec(app_id):
    """Return (spec, err). BV-BRC specs are read by a lenient Perl JSON loader
    and some carry trailing commas."""
    path = os.path.join(APPS, ALIAS.get(app_id, app_id) + ".json")
    if not os.path.exists(path):
        return None, "no app spec in all_app_specs"
    raw = open(path, encoding="utf-8", errors="replace").read()
    try:
        return json.loads(raw), None
    except json.JSONDecodeError:
        try:
            return json.loads(re.sub(r",(\s*[}\]])", r"\1", raw)), None
        except json.JSONDecodeError as e:
            return None, f"app spec not valid JSON ({e})"


def record_params(spec):
    """Group params, plus list-typed params that take multiple values."""
    out = []
    for p in spec.get("parameters", []):
        if p.get("type") == "group" or (
            isinstance(p.get("type"), list) and p.get("allow_multiple")
        ):
            out.append(p)
    return out


def main():
    rows = sqlite3.connect(f"file:{DB}?mode=ro", uri=True).execute(
        "SELECT id, name, raw_cwl FROM workflows ORDER BY name"
    ).fetchall()

    problems = []
    print(f"{'registered workflow':<32} {'app_id':<30} verdict")
    print("-" * 100)

    for wf_id, name, raw_cwl in rows:
        m = re.search(r"bvbrc_app_id:\s*(\S+)", raw_cwl or "")
        if not m:
            print(f"{name:<32} {'(none)':<30} not a BV-BRC app workflow - skipped")
            continue
        app_id = m.group(1)

        spec, err = load_app_spec(app_id)
        if err:
            print(f"{name:<32} {app_id:<30} {err.upper()}")
            problems.append((name, app_id, err))
            continue

        try:
            d = json.load(urllib.request.urlopen(
                f"{GOWE}/api/v1/workflows/{wf_id}/inputs", timeout=30))
            inputs = {i["id"]: i for i in (d.get("data") or d.get("inputs") or [])}
        except Exception as e:
            print(f"{name:<32} {app_id:<30} could not read GoWe inputs: {e}")
            problems.append((name, app_id, f"GoWe /inputs failed: {e}"))
            continue

        groups = record_params(spec)
        if not groups:
            print(f"{name:<32} {app_id:<30} ok - app declares no record params")
            continue

        for g in groups:
            gid = g["id"]
            app_fields = sorted(f["id"] for f in (g.get("group") or []))
            if gid not in inputs:
                print(f"{name:<32} {app_id:<30} {gid}: MISSING from the registered CWL")
                problems.append((name, app_id, f"{gid}: missing from CWL"))
                continue
            gi = inputs[gid]
            t = gi.get("type", "")
            cwl_fields = sorted(f["name"] for f in (gi.get("fields") or []))
            if not app_fields:
                # A list-typed multi-value param (e.g. GeneTree's `sequences`,
                # whose type is a list of accepted data types) declares no
                # sub-fields in the app spec, so there is nothing to compare.
                # BV-BRC's convention is that each value is a hash -- confirmed
                # for GeneTree in App-GeneTree.pl, which reads {filename} and
                # {type}. Only check that the CWL declares a record at all.
                verdict = ("ok (record, %d fields; app spec declares no "
                           "sub-fields to compare)" % len(cwl_fields)
                           if "record" in t else
                           "NOT a record (type=%s)" % t)
                print(f"{name:<32} {app_id:<30} {gid}: {verdict}")
                if "record" not in t:
                    problems.append((name, app_id, f"{gid}: CWL type {t}"))
                continue
            if "record" not in t:
                print(f"{name:<32} {app_id:<30} {gid}: NOT a record (type={t})")
                problems.append((name, app_id, f"{gid}: CWL type {t}"))
            elif not cwl_fields:
                print(f"{name:<32} {app_id:<30} {gid}: zero fields parsed "
                      f"(array-of-records parser fix missing?)")
                problems.append((name, app_id, f"{gid}: no fields parsed"))
            elif app_fields != cwl_fields:
                missing = [f for f in app_fields if f not in cwl_fields]
                extra = [f for f in cwl_fields if f not in app_fields]
                print(f"{name:<32} {app_id:<30} {gid}: FIELD MISMATCH")
                if missing:
                    print(f"{'':<63} app has, CWL lacks: {missing}")
                if extra:
                    print(f"{'':<63} CWL has, app lacks: {extra}")
                problems.append((name, app_id,
                                 f"{gid}: missing={missing} extra={extra}"))
            else:
                print(f"{name:<32} {app_id:<30} {gid}: ok ({len(cwl_fields)} fields)")

    print()
    print(f"=== {len(problems)} problem(s) across {len(rows)} registered workflows ===")
    for n, a, p in problems:
        print(f"  {n} [{a}]: {p}")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
