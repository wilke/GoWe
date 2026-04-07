#!/usr/bin/env python3
"""Submit a workflow job and poll until completion.

Usage:
    python submit_and_poll.py <workflow_id_or_name> <inputs.json> [--output-dest <uri>]
    python submit_and_poll.py protein-structure-prediction inputs.json
    python submit_and_poll.py wf_abc123 inputs.json --output-dest "ws:///user@bvbrc/home/results/"
    GOWE_URL=http://myhost:9090 python submit_and_poll.py wf_abc123 inputs.json

Environment:
    GOWE_URL        Base URL (default: http://localhost:8091)
    BVBRC_TOKEN     BV-BRC token for authentication
    GOWE_POLL       Poll interval in seconds (default: 10)
"""

import json
import os
import sys
import time
from common import api, pp

# Parse arguments
if len(sys.argv) < 3:
    print("Usage: python submit_and_poll.py <workflow_id_or_name> <inputs.json> [--output-dest <uri>]", file=sys.stderr)
    sys.exit(1)

workflow_id = sys.argv[1]
inputs_file = sys.argv[2]
output_dest = None
poll_interval = int(os.environ.get("GOWE_POLL", "10"))

i = 3
while i < len(sys.argv):
    if sys.argv[i] == "--output-dest" and i + 1 < len(sys.argv):
        output_dest = sys.argv[i + 1]
        i += 2
    else:
        print(f"Unknown argument: {sys.argv[i]}", file=sys.stderr)
        sys.exit(1)

with open(inputs_file) as f:
    inputs = json.load(f)

# Step 1: Dry run
print("Validating...")
validation = api("POST", "/submissions", body={"workflow_id": workflow_id, "inputs": inputs}, params={"dry_run": "true"})
result = validation["data"]
if not result["valid"]:
    print("Validation failed:")
    for e in result.get("errors", []):
        print(f"  {e['field']}: {e['message']}")
    sys.exit(1)
print(f"  Workflow: {result['workflow']['name']} ({result['workflow']['step_count']} steps)")
print(f"  Execution order: {' -> '.join(result['execution_order'])}")

# Step 2: Submit
print("\nSubmitting...")
body = {"workflow_id": workflow_id, "inputs": inputs}
if output_dest:
    body["output_destination"] = output_dest

resp = api("POST", "/submissions", body=body)
sub = resp["data"]
sub_id = sub["id"]
print(f"  Submission: {sub_id}")
print(f"  State: {sub['state']}")

# Step 3: Poll
terminal_states = {"COMPLETED", "FAILED", "CANCELLED"}
print(f"\nPolling every {poll_interval}s...")

while True:
    time.sleep(poll_interval)
    resp = api("GET", f"/submissions/{sub_id}")
    sub = resp["data"]
    ts = sub.get("task_summary", {})
    progress = f"tasks: {ts.get('success', 0)}/{ts.get('total', 0)} done, {ts.get('running', 0)} running, {ts.get('failed', 0)} failed"
    print(f"  [{sub['state']}] {progress}")

    if sub["state"] in terminal_states:
        break

# Step 4: Report
print(f"\nFinal state: {sub['state']}")
if sub["state"] == "COMPLETED":
    print("\nOutputs:")
    pp(sub.get("outputs", {}))
elif sub["state"] == "FAILED":
    print("\nChecking task logs for failures...")
    tasks_resp = api("GET", f"/submissions/{sub_id}/tasks", params={"limit": "100"})
    for task in tasks_resp["data"]:
        if task["state"] == "FAILED":
            print(f"\n  Task {task['id']} (step: {task['step_id']}):")
            logs_resp = api("GET", f"/submissions/{sub_id}/tasks/{task['id']}/logs")
            logs = logs_resp["data"]
            if logs.get("stderr"):
                print(f"    stderr: {logs['stderr'][:500]}")
            print(f"    exit_code: {logs.get('exit_code', '?')}")
