#!/usr/bin/env python3
"""Audit every registered workflow's CWL against its BV-BRC app spec.

`diff_app_specs.py` compares record/group parameters only. This covers every
dimension where a disagreement can cost a submission, and emits a per-service
report of what needs changing and by whom.

The two failure directions matter equally:

  TOO LOOSE  our CWL permits something the app rejects -> the agent builds it
             in good faith, GoWe accepts, BV-BRC fails minutes later.
  TOO STRICT our CWL or GoWe rejects something the app accepts -> a valid
             request is refused at submit. 10 payloads that ran to completion
             are currently refused this way.

Checks, per workflow:
  1. required by the app, absent from our CWL
  2. required by the app, optional in our CWL          (the MSA SNP type bug)
  3. app declares an enum, our doc has no [enum: ...]  (unvalidatable)
  4. our enum values disagree with the app's           (Docking named_library)
  5. in our CWL, unknown to the app
  6. allow_multiple vs array-ness
  7. record field sets differ                          (the replay's 10)

Keyed on the `bvbrc_app_id` hint in each workflow's own raw CWL. Never match by
input name -- doing so produced a 20-item list of pure artefacts once already
(see the comment in diff_app_specs.py).

    python3 scripts/audit_specs.py                 # human-readable
    python3 scripts/audit_specs.py --markdown      # the per-service report
"""

from __future__ import annotations

import argparse
import collections
import json
import os
import re
import sqlite3
import sys
import urllib.request

DB = os.path.expanduser(os.environ.get("GOWE_DB", "~/GoWeServer/gowe.db"))
GOWE = os.environ.get("GOWE_URL", "http://localhost:12007")
SPECS = os.environ.get(
    "APP_SPECS", os.path.expanduser("~/bvbrc-dev/Copilot/all_app_specs"))
# The website app forms are the best contract available: they carry enum values
# and payload shapes that appear in no spec. Used here to tell a REAL "we offer
# a value the app rejects" from a stale mirrored spec -- proven necessary on
# Viral Assembly (7 inputs absent from the spec, all 7 sent by the form) and on
# Variation (we offer Snippy; the spec omits it, the form references it 11
# times, so our CWL is right and the spec is behind).
FORMS = os.environ.get("APP_FORMS", os.path.expanduser(
    "~/bvbrc-dev/Copilot/Agents/bvbrc_website/public/js/p3/widget/app"))

ENUM_DOC = re.compile(r"\[enum:([^\]]*)\]")


def load_spec(app_id):
    p = os.path.join(SPECS, app_id + ".json")
    if not os.path.exists(p):
        return None, "no app spec in the mirror"
    raw = open(p).read()
    try:
        return json.loads(raw), None
    except json.JSONDecodeError:
        # Several specs carry trailing commas; one uses # comments and stays
        # invalid even after this repair (ComprehensiveSARS2Analysis).
        try:
            return json.loads(re.sub(r",(\s*[}\]])", r"\1", raw)), None
        except json.JSONDecodeError as e:
            return None, f"app spec is not valid JSON ({e.msg})"


_form_cache: dict[str, str] = {}


def form_text(app_id):
    """Every website source file whose name resembles this app, concatenated.

    Matched loosely on purpose: the form file is Variation.js for Variation but
    Assembly2.js for GenomeAssembly2, and templates/<name>.html carries the
    radio values. A miss returns "" and the caller reports "unverified" rather
    than asserting anything.
    """
    if app_id in _form_cache:
        return _form_cache[app_id]
    stem = app_id.replace("2", "")
    out = []
    for root, _dirs, files in os.walk(FORMS):
        for fn in files:
            base = os.path.splitext(fn)[0]
            if base.lower().startswith(stem.lower()[:8]) or \
               stem.lower().startswith(base.lower()[:8]):
                try:
                    out.append(open(os.path.join(root, fn),
                                    errors="replace").read())
                except OSError:
                    pass
    _form_cache[app_id] = "\n".join(out)
    return _form_cache[app_id]


def in_form(app_id, value):
    """Tri-state: True seen in the form, False not seen, None no form found."""
    txt = form_text(app_id)
    if not txt:
        return None
    return str(value) in txt


def doc_symbols(doc):
    m = ENUM_DOC.search(doc or "")
    return [v.strip() for v in m.group(1).split(",") if v.strip()] if m else []


def audit():
    rows = sqlite3.connect(f"file:{DB}?mode=ro", uri=True).execute(
        "SELECT id, name, raw_cwl FROM workflows ORDER BY name").fetchall()
    findings = collections.defaultdict(list)   # workflow -> [(kind, detail)]
    skipped = []

    for wf_id, name, raw_cwl in rows:
        m = re.search(r"bvbrc_app_id:\s*(\S+)", raw_cwl or "")
        if not m:
            skipped.append((name, "not a BV-BRC app workflow"))
            continue
        app_id = m.group(1)
        spec, err = load_spec(app_id)
        if err:
            findings[name].append(("spec-unreadable", f"{app_id}: {err}"))
            continue
        try:
            d = json.load(urllib.request.urlopen(
                f"{GOWE}/api/v1/workflows/{wf_id}/inputs", timeout=30))
            ours = {i["id"]: i for i in (d.get("data") or [])}
        except Exception as e:
            findings[name].append(("gowe-unreadable", str(e)))
            continue

        params = {p["id"]: p for p in spec.get("parameters", [])
                  if isinstance(p, dict) and "id" in p}

        for pid, p in params.items():
            app_required = str(p.get("required", 0)) in ("1", "True", "true")
            mine = ours.get(pid)

            if mine is None:
                if app_required:
                    findings[name].append((
                        "missing-required",
                        f"`{pid}` is required by the app but absent from our CWL"))
                continue

            # 2. Required by the app and genuinely omissible for us.
            #
            # GET /inputs reports required=False for anything carrying a
            # default, but MergeWorkflowInputDefaults fills those before
            # dispatch, so the app always receives a value and there is no
            # problem. Only an input with NO default can actually reach the
            # app absent.
            #
            # Comparing required flags alone flagged MSA SNP alphabet
            # (default "dna"), Metagenomic Binning force_local_assembly
            # (default false) and Docking batch_size (default 10) as bugs.
            # All three were artefacts -- the fourth artefact class this
            # script produced.
            if app_required and not mine.get("required") \
                    and mine.get("default") is None:
                findings[name].append((
                    "required-mismatch",
                    f"`{pid}` is required by the app, optional for us, and has "
                    f"NO default — it can reach the app absent (type "
                    f"{mine['type']})"))

            # 3/4. enums.
            # Normalise to strings: a spec may write a numeric enum as
            # [0, 0.1, 1] while our doc writes ["0", "0.1", "1"]. Comparing
            # raw values reported identical sets as both too loose and too
            # strict -- the first artefact this script produced.
            app_enum = [str(v) for v in (p.get("enum") or [])]
            ours_enum = [str(v) for v in
                         (mine.get("symbols") or doc_symbols(mine.get("doc")))]
            if app_enum and not ours_enum:
                findings[name].append((
                    "enum-undeclared",
                    f"`{pid}`: app declares enum {app_enum} — our doc has no "
                    f"`[enum: ...]`, so it cannot be validated"))
            elif app_enum and ours_enum:
                extra = [v for v in ours_enum if v not in app_enum]
                absent = [v for v in app_enum if v not in ours_enum]
                if extra:
                    seen = [v for v in extra if in_form(app_id, v)]
                    unseen = [v for v in extra if in_form(app_id, v) is False]
                    if seen and not unseen:
                        findings[name].append((
                            "spec-behind-form",
                            f"`{pid}`: we offer {seen}, absent from the spec but "
                            f"present in the website form — our CWL is right, "
                            f"the spec copy is stale"))
                    elif unseen:
                        findings[name].append((
                            "enum-too-loose",
                            f"`{pid}`: we offer {unseen}, declared by neither the "
                            f"app spec nor the website form — likely invalid"))
                    else:
                        findings[name].append((
                            "enum-unverified",
                            f"`{pid}`: we offer {extra}, not in the spec; no "
                            f"website form found to check against"))
                if absent:
                    findings[name].append((
                        "enum-too-strict",
                        f"`{pid}`: the app allows {absent}, which we do not offer"))

            # 6. arity.
            # Only trust allow_multiple when the spec states it. Treating an
            # ABSENT key as false flagged seven arrays as too strict -- our
            # CWLs were right and the key was simply omitted.
            declared_multi = "allow_multiple" in p
            multi = str(p.get("allow_multiple", "")) in ("1", "True", "true")
            is_arr = mine["type"].rstrip("?").endswith("[]")
            if declared_multi and multi and not is_arr:
                findings[name].append((
                    "arity-mismatch",
                    f"`{pid}`: app allow_multiple=true, our type is "
                    f"{mine['type']} (not an array)"))
            if declared_multi and is_arr and not multi and p.get("type") != "group":
                findings[name].append((
                    "arity-mismatch",
                    f"`{pid}`: our type is {mine['type']} but the app declares "
                    f"allow_multiple=false"))

            # 7. record fields.
            app_fields = {f["id"] for f in (p.get("group") or [])
                          if isinstance(f, dict) and "id" in f}
            our_fields = {f["name"] for f in (mine.get("fields") or [])}
            if app_fields and our_fields and app_fields != our_fields:
                only_app = sorted(app_fields - our_fields)
                only_ours = sorted(our_fields - app_fields)
                bits = []
                if only_app:
                    bits.append(f"app-only {only_app}")
                if only_ours:
                    bits.append(f"ours-only {only_ours}")
                findings[name].append((
                    "record-fields", f"`{pid}`: " + "; ".join(bits)))

            # enums on record fields
            for f in (p.get("group") or []):
                if not (isinstance(f, dict) and f.get("enum")):
                    continue
                mf = next((x for x in (mine.get("fields") or [])
                           if x["name"] == f["id"]), None)
                if mf is None:
                    continue
                got = mf.get("symbols") or doc_symbols(mf.get("doc"))
                if not got:
                    findings[name].append((
                        "enum-undeclared",
                        f"`{pid}.{f['id']}`: app declares enum {f['enum']} — "
                        f"our doc has no `[enum: ...]`"))

            # 5. ours, unknown to the app.
        for oid in ours:
            if oid in params:
                continue
            if oid.startswith("_") or oid in ("output_path", "output_file"):
                continue
            findings[name].append((
                "mirror-may-be-stale",
                f"`{oid}` is in our CWL but not in the mirrored app spec — "
                f"check `bvbrc_website/public/js/p3/widget/app/` before "
                f"concluding anything"))

    return findings, skipped


SEVERITY = {
    "missing-required": ("BLOCKS SUBMISSION", "the app will reject the job"),
    "required-mismatch": ("TOO LOOSE", "the agent may omit it in good faith"),
    "enum-too-loose": ("TOO LOOSE", "we offer a value the app rejects"),
    "enum-undeclared": ("UNVALIDATED", "a wrong value reaches BV-BRC silently"),
    "enum-too-strict": ("TOO STRICT", "a valid request is refused"),
    "spec-behind-form": (
        "SPEC IS STALE",
        "our CWL matches the website form; the mirrored spec is out of date"),
    "enum-unverified": ("UNVERIFIED", "no website form found to cross-check"),
    "arity-mismatch": ("TOO STRICT", "a valid shape is refused"),
    "record-fields": ("TOO STRICT", "a valid field is refused"),
    "mirror-may-be-stale": (
        "CHECK THE FORM",
        "absent from our spec COPY; the deployed app may still accept it. "
        "Proven on Viral Assembly: 7 inputs absent from the spec, all 7 sent "
        "by the website form"),
    "spec-unreadable": ("CANNOT AUDIT", "no usable app spec"),
    "gowe-unreadable": ("CANNOT AUDIT", "GoWe did not answer"),
}
ORDER = list(SEVERITY)


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--markdown", action="store_true")
    a = ap.parse_args()

    findings, skipped = audit()
    total = sum(len(v) for v in findings.values())

    if not a.markdown:
        for wf in sorted(findings):
            print(f"\n{wf}")
            for kind, detail in sorted(findings[wf], key=lambda x: ORDER.index(x[0])):
                print(f"   [{SEVERITY[kind][0]:17}] {detail}")
        for n, why in skipped:
            print(f"\n{n}\n   (skipped: {why})")
        print(f"\n=== {total} finding(s) across {len(findings)} workflow(s) ===")
        return 1 if total else 0

    by_kind = collections.Counter(k for v in findings.values() for k, _ in v)
    print("# CWL vs BV-BRC app spec — per-service audit\n")
    print("Generated by `GoWe/scripts/audit_specs.py`. Keyed on each workflow's")
    print("own `bvbrc_app_id` hint, never by input name.\n")
    print("| Class | Count | Means |")
    print("|---|---:|---|")
    for k in ORDER:
        if by_kind[k]:
            print(f"| {SEVERITY[k][0]} — `{k}` | {by_kind[k]} | {SEVERITY[k][1]} |")
    print(f"\n**{total} findings across {len(findings)} workflows.**\n")
    for wf in sorted(findings):
        print(f"\n## {wf}\n")
        for kind, detail in sorted(findings[wf], key=lambda x: ORDER.index(x[0])):
            print(f"- **{SEVERITY[kind][0]}** — {detail}")
    if skipped:
        print("\n## Not audited\n")
        for n, why in skipped:
            print(f"- **{n}** — {why}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
