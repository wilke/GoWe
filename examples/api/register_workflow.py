#!/usr/bin/env python3
"""Register tools and compose a workflow that references them.

Demonstrates the full flow:
  1. Register individual CWL CommandLineTools
  2. Create a Workflow that references them via gowe:// URIs
  3. Inspect the resulting workflow inputs

Usage:
    python register_workflow.py
    GOWE_URL=http://myhost:9090 python register_workflow.py

The gowe:// scheme lets workflows reference tools already registered in the
server by name or ID:
    run: gowe://my-tool-name      # lookup by name
    run: gowe://wf_abc123         # lookup by ID
The server resolves these at registration time by inlining the tool CWL into
a packed $graph document.
"""

import textwrap
from common import api, pp

# ---------------------------------------------------------------------------
# Step 1: Register two standalone CommandLineTools
# ---------------------------------------------------------------------------

print("=== Step 1: Register tools ===\n")

# A simple grep tool
grep_cwl = textwrap.dedent("""\
    cwlVersion: v1.2
    class: CommandLineTool
    baseCommand: grep
    arguments: ["-c"]
    inputs:
      pattern:
        type: string
        inputBinding:
          position: 1
      file:
        type: File
        inputBinding:
          position: 2
    outputs:
      count:
        type: stdout
    stdout: count.txt
""")

resp = api("POST", "/workflows", body={
    "name": "grep-count",
    "description": "Count lines matching a pattern",
    "cwl": grep_cwl,
})
grep_tool = resp["data"]
print(f"  Registered: {grep_tool['name']} ({grep_tool['id']}, class={grep_tool['class']})")

# A simple word-count tool
wc_cwl = textwrap.dedent("""\
    cwlVersion: v1.2
    class: CommandLineTool
    baseCommand: wc
    arguments: ["-w"]
    inputs:
      file:
        type: File
        inputBinding:
          position: 1
    outputs:
      count:
        type: stdout
    stdout: wc.txt
""")

resp = api("POST", "/workflows", body={
    "name": "word-count",
    "description": "Count words in a file",
    "cwl": wc_cwl,
})
wc_tool = resp["data"]
print(f"  Registered: {wc_tool['name']} ({wc_tool['id']}, class={wc_tool['class']})")

# ---------------------------------------------------------------------------
# Step 2: Create a Workflow that references the tools via gowe://
# ---------------------------------------------------------------------------

print("\n=== Step 2: Create workflow using gowe:// references ===\n")

# The 'run' fields use gowe://<name> to reference the tools we just registered.
# The server resolves these into a packed $graph at registration time.
workflow_cwl = textwrap.dedent("""\
    cwlVersion: v1.2
    class: Workflow

    inputs:
      input_file:
        type: File
      grep_pattern:
        type: string

    outputs:
      grep_result:
        type: File
        outputSource: grep_step/count
      wc_result:
        type: File
        outputSource: wc_step/count

    steps:
      grep_step:
        run: gowe://grep-count
        in:
          pattern: grep_pattern
          file: input_file
        out: [count]

      wc_step:
        run: gowe://word-count
        in:
          file: input_file
        out: [count]
""")

resp = api("POST", "/workflows", body={
    "name": "text-analysis",
    "description": "Run grep and word-count on an input file (parallel steps)",
    "cwl": workflow_cwl,
    "labels": {"category": "example"},
})
workflow = resp["data"]
print(f"  Registered: {workflow['name']} ({workflow['id']}, class={workflow['class']})")
print(f"  Steps: {workflow.get('step_count', len(workflow.get('steps', [])))}")

# ---------------------------------------------------------------------------
# Step 3: Inspect the workflow's input definitions
# ---------------------------------------------------------------------------

print("\n=== Step 3: Workflow inputs ===\n")

resp = api("GET", f"/workflows/{workflow['id']}")
wf = resp["data"]

for inp in wf.get("inputs", []):
    req = "required" if inp["required"] else "optional"
    print(f"  {inp['id']}: {inp['type']} ({req})")

# ---------------------------------------------------------------------------
# Step 4: Dry-run to verify everything resolves
# ---------------------------------------------------------------------------

print("\n=== Step 4: Dry-run validation ===\n")

resp = api("POST", "/submissions", body={
    "workflow_id": workflow["id"],
    "inputs": {
        "input_file": {"class": "File", "location": "/tmp/test.txt"},
        "grep_pattern": "hello",
    },
}, params={"dry_run": "true"})
result = resp["data"]

print(f"  Valid: {result['valid']}")
print(f"  DAG acyclic: {result['dag_acyclic']}")
print(f"  Execution order: {' -> '.join(result['execution_order'])}")
for step in result.get("steps", []):
    avail = "ok" if step["executor_available"] else "MISSING"
    print(f"  Step '{step['id']}': executor={step['executor_type']} [{avail}]")

errors = result.get("errors", [])
if errors:
    print("\n  Errors:")
    for e in errors:
        print(f"    {e['field']}: {e['message']}")
else:
    print("\n  Ready to submit!")

# ---------------------------------------------------------------------------
# Cleanup hint
# ---------------------------------------------------------------------------

print(f"\n=== Registered resources ===")
print(f"  Tool:     {grep_tool['id']} ({grep_tool['name']})")
print(f"  Tool:     {wc_tool['id']} ({wc_tool['name']})")
print(f"  Workflow: {workflow['id']} ({workflow['name']})")
print(f"\nTo submit: python submit_and_poll.py {workflow['name']} <inputs.json>")
