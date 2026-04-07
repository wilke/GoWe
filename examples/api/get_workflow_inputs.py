#!/usr/bin/env python3
"""Show input definitions for a workflow.

Usage:
    python get_workflow_inputs.py <workflow_id_or_name>
    python get_workflow_inputs.py protein-structure-prediction
    GOWE_URL=http://myhost:9090 python get_workflow_inputs.py wf_abc123
"""

import sys
from common import api

if len(sys.argv) < 2:
    print("Usage: python get_workflow_inputs.py <workflow_id_or_name>", file=sys.stderr)
    sys.exit(1)

workflow_id = sys.argv[1]
resp = api("GET", f"/workflows/{workflow_id}")
wf = resp["data"]

print(f"Workflow:  {wf['name']} ({wf['id']})")
print(f"Class:    {wf['class']}")
print(f"Steps:    {len(wf.get('steps', []))}")
print()

inputs = wf.get("inputs", [])
if not inputs:
    print("No inputs defined.")
    sys.exit(0)

print(f"{'Input':<30} {'Type':<20} {'Required':<10} {'Default'}")
print(f"{'─' * 30} {'─' * 20} {'─' * 10} {'─' * 20}")
for inp in inputs:
    name = inp["id"]
    typ = inp["type"]
    req = "yes" if inp["required"] else "no"
    default = inp.get("default")
    default_str = str(default) if default is not None else "—"
    print(f"{name:<30} {typ:<20} {req:<10} {default_str}")

print(f"\n{len(inputs)} input(s)")

# Show outputs too
outputs = wf.get("outputs", [])
if outputs:
    print(f"\nOutputs:")
    for out in outputs:
        print(f"  {out['id']}: {out['type']}")
