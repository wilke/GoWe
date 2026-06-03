cwlVersion: v1.2
class: CommandLineTool

doc: "Extract genome_id from a BV-BRC .genome file's workspace autometadata. Polls until the autometadata is populated (computed asynchronously after file upload)."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

requirements:
  InlineJavascriptRequirement: {}
  InitialWorkDirRequirement:
    listing:
      - entryname: get_genome_id.py
        entry: |
          #!/usr/bin/env python3
          """Fetch genome_id from BV-BRC workspace autometadata.

          The workspace runs ws-autometa-genome.pl after a .genome file is saved,
          which extracts genome_id from the GTO's 'id' field and stores it in the
          object's auto_metadata. We read that metadata (lightweight) instead of
          downloading the full GTO (which contains all contigs + features).

          Polls with retry because autometadata computation may lag file creation.
          """
          import json, os, sys, time, urllib.request

          WS_URL = "https://p3.theseed.org/services/Workspace"
          MAX_ATTEMPTS = 20
          POLL_INTERVAL = 30

          def ws_get_metadata(ws_path, token):
              payload = json.dumps({
                  "id": "1", "method": "Workspace.get", "version": "1.1",
                  "params": [{"objects": [ws_path], "metadata_only": True}]
              }).encode()
              req = urllib.request.Request(WS_URL, data=payload, headers={
                  "Content-Type": "application/json",
                  "Authorization": "OAuth " + token,
              })
              resp = urllib.request.urlopen(req)
              result = json.loads(resp.read())
              if "error" in result:
                  raise RuntimeError(result["error"].get("message", str(result["error"])))
              # Tuple: [path, type, owner, time, id, owner_id, size, user_meta, auto_meta, ...]
              obj = result["result"][0][0]
              auto_meta = obj[8] if len(obj) > 8 else {}
              return auto_meta

          def main():
              ws_path = sys.argv[1]
              token = os.environ.get("BVBRC_TOKEN", "")
              if not token:
                  print("ERROR: BVBRC_TOKEN not set", file=sys.stderr)
                  sys.exit(1)

              for attempt in range(1, MAX_ATTEMPTS + 1):
                  try:
                      meta = ws_get_metadata(ws_path, token)
                  except Exception as e:
                      print(f"Attempt {attempt}: workspace error: {e}", file=sys.stderr)
                      if attempt < MAX_ATTEMPTS:
                          time.sleep(POLL_INTERVAL)
                          continue
                      sys.exit(1)

                  genome_id = meta.get("genome_id", "")
                  if genome_id:
                      print(genome_id)
                      return

                  print(f"Attempt {attempt}: genome_id not yet in autometadata, waiting...", file=sys.stderr)
                  if attempt < MAX_ATTEMPTS:
                      time.sleep(POLL_INTERVAL)

              print("ERROR: genome_id not found after max attempts", file=sys.stderr)
              sys.exit(1)

          if __name__ == "__main__":
              main()

hints:
  DockerRequirement:
    dockerPull: "python.sif"
  gowe:Execution:
    executor: worker
    inject_bvbrc_token: true

baseCommand: ["python3", "get_genome_id.py"]

inputs:
  genome_ws_path:
    type: string
    doc: "Workspace path to the .genome GTO file"
    inputBinding:
      position: 1

stdout: genome_id.txt

outputs:
  genome_id:
    type: string
    outputBinding:
      glob: genome_id.txt
      loadContents: true
      outputEval: $(self[0].contents.trim())
