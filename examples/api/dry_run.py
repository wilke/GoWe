#!/usr/bin/env python3
"""Validate a submission without running it (dry run).

Usage:
    python dry_run.py <workflow_id_or_name> <inputs.json>
    python dry_run.py protein-structure-prediction inputs.json
    GOWE_URL=http://myhost:9090 python dry_run.py wf_abc123 inputs.json
"""

import json
import sys
from common import api, pp

if len(sys.argv) < 3:
    print("Usage: python dry_run.py <workflow_id_or_name> <inputs.json>", file=sys.stderr)
    sys.exit(1)

workflow_id = sys.argv[1]
inputs_file = sys.argv[2]

with open(inputs_file) as f:
    inputs = json.load(f)

resp = api("POST", "/submissions", body={"workflow_id": workflow_id, "inputs": inputs}, params={"dry_run": "true"})
result = resp["data"]

valid = result["valid"]
print(f"Valid: {valid}")
print(f"Workflow: {result['workflow']['name']} ({result['workflow']['id']}, {result['workflow']['step_count']} steps)")
print(f"DAG acyclic: {result['dag_acyclic']}")
print(f"Execution order: {' -> '.join(result['execution_order'])}")
print()

# Executor availability
print("Executors:")
for executor, status in result.get("executor_availability", {}).items():
    marker = "+" if status == "available" else "-"
    print(f"  [{marker}] {executor}: {status}")
print()

# Steps
print("Steps:")
for step in result.get("steps", []):
    avail = "ok" if step["executor_available"] else "MISSING"
    deps = ", ".join(step["depends_on"]) if step["depends_on"] else "none"
    print(f"  {step['id']}: executor={step['executor_type']} deps=[{deps}] {avail}")
print()

# Errors and warnings
errors = result.get("errors", [])
warnings = result.get("warnings", [])
if errors:
    print("Errors:")
    for e in errors:
        print(f"  {e['field']}: {e['message']}")
if warnings:
    print("Warnings:")
    for w in warnings:
        print(f"  {w['field']}: {w['message']}")

if valid and not errors:
    print("Validation passed — ready to submit.")
else:
    print("Validation failed — fix errors before submitting.")
    sys.exit(1)
