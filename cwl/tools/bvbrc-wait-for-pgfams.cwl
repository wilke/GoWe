cwlVersion: v1.2
class: CommandLineTool

label: bvbrc-wait-for-pgfams

doc: |
  Wait until a BV-BRC genome has PGFam-tagged CDS features indexed in solr.
  Required before submitting CodonTree on freshly-annotated genomes — CGA's
  annotation produces the .genome object but PGFam assignment is a separate
  downstream indexing step that completes asynchronously (typically minutes
  to ~1 hour after annotation finishes).

  Polls the BV-BRC data API (genome_feature collection) and exits once at
  least `min_pgfam_count` CDS records for the genome have a non-empty
  pgfam_id. Passes the genome_id through as output so it can chain with
  downstream tree-building steps.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

requirements:
  InlineJavascriptRequirement: {}
  NetworkAccess:
    networkAccess: true
  InitialWorkDirRequirement:
    listing:
      - entryname: wait_for_pgfams.py
        entry: |
          #!/usr/bin/env python3
          """Poll BV-BRC data API until a genome's CDS features have PGFam IDs.

          CodonTree's verify_genome_ids (App-CodonTree.pl) rejects any main
          genome whose PGFam alignment set would be empty. Freshly annotated
          genomes pass annotation but their PGFam assignment is propagated to
          solr asynchronously. This polls genome_feature with select(pgfam_id)
          and counts records whose pgfam_id is set.
          """
          import json, os, sys, time, urllib.request, urllib.parse

          DATA_API = "https://www.patricbrc.org/api/genome_feature/"

          def count_pgfam_features(genome_id, token, sample_size=200):
              query = (
                  "eq(genome_id,{gid})"
                  "&eq(feature_type,CDS)"
                  "&select(pgfam_id)"
                  "&limit({n})"
              ).format(gid=genome_id, n=sample_size)
              url = DATA_API + "?" + query
              req = urllib.request.Request(url, headers={
                  "Authorization": token,
                  "Accept": "application/json",
              })
              with urllib.request.urlopen(req, timeout=30) as resp:
                  records = json.loads(resp.read())
              if not isinstance(records, list):
                  return 0
              return sum(1 for r in records if r.get("pgfam_id"))

          def main():
              genome_id = sys.argv[1]
              min_count = int(sys.argv[2]) if len(sys.argv) > 2 else 10
              timeout_s = int(sys.argv[3]) if len(sys.argv) > 3 else 1800
              poll_s = int(sys.argv[4]) if len(sys.argv) > 4 else 30

              token = os.environ.get("BVBRC_TOKEN", "")
              if not token:
                  print("ERROR: BVBRC_TOKEN not set", file=sys.stderr)
                  sys.exit(1)

              deadline = time.monotonic() + timeout_s
              attempt = 0
              while True:
                  attempt += 1
                  try:
                      n = count_pgfam_features(genome_id, token)
                  except Exception as e:
                      print("Attempt {}: API error: {}".format(attempt, e),
                            file=sys.stderr)
                      n = -1

                  if n >= min_count:
                      print("PGFam features ready for {}: {} (>= {}) after {} attempts".format(
                          genome_id, n, min_count, attempt), file=sys.stderr)
                      print(genome_id)
                      return

                  remaining = deadline - time.monotonic()
                  if remaining <= 0:
                      print("ERROR: timeout after {}s waiting for PGFams on {} (last count: {})".format(
                          timeout_s, genome_id, n), file=sys.stderr)
                      sys.exit(1)

                  print("Attempt {}: pgfam-tagged CDS count = {} (need {}), sleeping {}s...".format(
                      attempt, n, min_count, poll_s), file=sys.stderr)
                  time.sleep(min(poll_s, remaining))

          if __name__ == "__main__":
              main()

hints:
  DockerRequirement:
    dockerPull: "python.sif"
  gowe:Execution:
    executor: worker
    inject_bvbrc_token: true

baseCommand: ["python3", "wait_for_pgfams.py"]

inputs:
  genome_id:
    type: string
    doc: "BV-BRC genome ID to wait on (e.g. 83332.1418)"
    inputBinding:
      position: 1
  min_pgfam_count:
    type: int?
    default: 10
    doc: "Minimum number of PGFam-tagged CDS features (in a 200-record sample) before considering the genome ready"
    inputBinding:
      position: 2
  timeout_seconds:
    type: int?
    default: 1800
    doc: "Maximum total wait time in seconds (default 30 minutes)"
    inputBinding:
      position: 3
  poll_interval_seconds:
    type: int?
    default: 30
    doc: "Seconds between polls"
    inputBinding:
      position: 4

stdout: genome_id.txt

outputs:
  genome_id:
    type: string
    doc: "Passthrough of the input genome_id once PGFam indexing is confirmed"
    outputBinding:
      glob: genome_id.txt
      loadContents: true
      outputEval: $(self[0].contents.trim())
