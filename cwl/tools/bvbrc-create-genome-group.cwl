cwlVersion: v1.2
class: CommandLineTool

doc: "Create a BV-BRC genome group in workspace from a list of genome IDs. The genome group is a workspace object of type genome_group containing {id_list: {genome_id: [...]}}."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

requirements:
  InlineJavascriptRequirement: {}
  NetworkAccess:
    networkAccess: true
  InitialWorkDirRequirement:
    listing:
      - entryname: create_genome_group.py
        entry: |
          #!/usr/bin/env python3
          """Create a BV-BRC genome group in workspace.

          A genome group is a workspace object with type 'genome_group' and content:
            {"id_list": {"genome_id": ["83332.12", "511145.12", ...]}}

          Verified from Workspace/internal-scripts/ws-autometa-genome_group.pl.
          """
          import json, os, sys, urllib.request

          WS_URL = "https://p3.theseed.org/services/Workspace"

          def main():
              group_name = sys.argv[1]
              ws_parent = sys.argv[2]
              genome_ids = sys.argv[3:]

              token = os.environ.get("BVBRC_TOKEN", "")
              if not token:
                  print("ERROR: BVBRC_TOKEN not set", file=sys.stderr)
                  sys.exit(1)

              group_path = ws_parent.rstrip("/") + "/" + group_name
              content = json.dumps({"id_list": {"genome_id": genome_ids}})

              payload = json.dumps({
                  "id": "1", "method": "Workspace.create", "version": "1.1",
                  "params": [{
                      "objects": [[group_path, "genome_group", {}, content]],
                      "overwrite": True,
                  }]
              }).encode()

              req = urllib.request.Request(WS_URL, data=payload, headers={
                  "Content-Type": "application/json",
                  "Authorization": token,
              })

              resp = urllib.request.urlopen(req)
              result = json.loads(resp.read())
              if "error" in result:
                  print(f"ERROR: {result['error'].get('message', result['error'])}", file=sys.stderr)
                  sys.exit(1)

              print(f"Created genome group: {group_path} with {len(genome_ids)} genome(s)", file=sys.stderr)
              print(group_path)

          if __name__ == "__main__":
              main()

hints:
  DockerRequirement:
    dockerPull: "python.sif"
  gowe:Execution:
    executor: worker
    inject_bvbrc_token: true

baseCommand: ["python3", "create_genome_group.py"]

inputs:
  group_name:
    type: string
    doc: "Name for the genome group"
    inputBinding:
      position: 1
  workspace_path:
    type: string
    doc: "Parent workspace folder path (e.g., /user@bvbrc/home/)"
    inputBinding:
      position: 2
  genome_ids:
    type: string[]
    doc: "BV-BRC genome IDs to include in the group"
    inputBinding:
      position: 3

stdout: group_path.txt

outputs:
  genome_group_path:
    type: string
    outputBinding:
      glob: group_path.txt
      loadContents: true
      outputEval: $(self[0].contents.trim())
