cwlVersion: v1.2
class: CommandLineTool
id: esviritu-summarize
label: Combine EsViritu results of several samples (summarize_esv_runs)
doc: |
  Collects the per-sample EsViritu tables and run YAMLs into one project
  directory and runs summarize_esv_runs. It writes the project-level detection,
  assembly, taxonomy, coverage and read-stats tables plus an HTML report.

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  DockerRequirement:
    dockerPull: esviritu-1.3.4-db3.2.4.sif
  ResourceRequirement:
    coresMin: 1
    ramMin: 4000

hints:
  gowe:Execution:
    executor: worker

baseCommand: [bash, -c]

arguments:
  - position: 0
    valueFrom: |-
      set -euo pipefail
      project="$1"; shift
      mkdir -p "$project" summary
      for d in "$@"; do
        for f in "$d"/*.tsv "$d"/*_esviritu.params.yaml "$d"/*_esviritu.readstats.yaml; do
          if [ -e "$f" ]; then cp "$f" "$project"/; fi
        done
      done
      summarize_esv_runs "$project" --outdir summary
      rm -rf "$project"
  - position: 1
    valueFrom: summarize

inputs:
  project:
    type: string
    label: Project name (prefix of the combined tables)
    inputBinding:
      position: 2
  sample_dirs:
    type: Directory[]
    label: Per-sample EsViritu output directories
    inputBinding:
      position: 3

outputs:
  summary_dir:
    type: Directory
    label: Combined project tables and report
    outputBinding:
      glob: summary
  detected_virus_info:
    type: File?
    outputBinding:
      glob: summary/$(inputs.project).detected_virus.info.tsv
  assembly_summary:
    type: File?
    outputBinding:
      glob: summary/$(inputs.project).detected_virus.assembly_summary.tsv
  tax_profile:
    type: File?
    outputBinding:
      glob: summary/$(inputs.project).tax_profile.tsv
  readstats:
    type: File?
    outputBinding:
      glob: summary/$(inputs.project).readstats.tsv
  report_html:
    type: File?
    outputBinding:
      glob: summary/*.html
