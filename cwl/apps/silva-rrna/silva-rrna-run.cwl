cwlVersion: v1.2
class: Workflow
id: silva-rrna-run
label: SILVA rRNA content for every sample of a sequencing run folder
doc: |
  Pairs the FASTQs in `run_path` (same rules as esviritu-run, reusing
  ../esviritu/pair-samples.cwl) and measures the rRNA content of each
  sample with silva-rrna.cwl. run_path, index and taxonomy are read in
  place on the worker, so they must be real paths under the worker's
  --extra-bind (not host symlinks).

requirements:
  ScatterFeatureRequirement: {}
  StepInputExpressionRequirement: {}
  InlineJavascriptRequirement: {}

inputs:
  run_path:
    type: string
  project_name:
    type: string?
  include_undetermined:
    type: boolean
    default: false
  index:
    type: string
    doc: minimap2 -x sr index (.mmi) of the SILVA SSU+LSU FASTA on the worker host.
  taxonomy:
    type: string
    doc: ID / gene / lineage TSV belonging to the index, on the worker host.
  max_pairs:
    type: int
    default: 0
    doc: Analyse only the first N pairs of each sample; 0 = all.
  min_aln_len:
    type: int
    default: 50
  min_identity:
    type: float
    default: 0.85
  threads:
    type: int
    default: 8
  ram_mb:
    type: int
    default: 8000

steps:
  pair:
    run: ../esviritu/pair-samples.cwl
    in:
      run_path: run_path
      project_name: project_name
      include_undetermined: include_undetermined
    out: [samples, r1, r2, run_dir, project]

  measure:
    run: silva-rrna.cwl
    scatter: [sample, r1, r2]
    scatterMethod: dotproduct
    in:
      sample: pair/samples
      r1: pair/r1
      r2: pair/r2
      run_path: pair/run_dir
      reads:
        valueFrom: |-
          ${
            var dir = inputs.run_path + "/";
            return [inputs.r1, inputs.r2].map(function (name) {
              return {"class": "File", "location": "file://" + dir + name,
                      "path": dir + name, "basename": name};
            });
          }
      index: index
      taxonomy: taxonomy
      max_pairs: max_pairs
      min_aln_len: min_aln_len
      min_identity: min_identity
      threads: threads
      ram_mb: ram_mb
    out: [summary, taxa, log]

outputs:
  samples:
    type: string[]
    outputSource: pair/samples
  summaries:
    type: File[]
    outputSource: measure/summary
  taxa:
    type: File[]
    outputSource: measure/taxa
  logs:
    type: File[]
    outputSource: measure/log
