cwlVersion: v1.2
class: Workflow
id: sortmerna-run
label: SortMeRNA rRNA content for every sample of a sequencing run folder
doc: |
  Pairs the FASTQs in `run_path` (same rules as esviritu-run, reusing
  ../esviritu/pair-samples.cwl) and measures the rRNA content of each
  sample with sortmerna.cwl. run_path, reference and index_dir are read in
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
  reference:
    type: string
    doc: rRNA reference FASTA on the worker host (e.g. smr_v4.3_default_db.fasta).
  index_dir:
    type: string
    doc: SortMeRNA index directory built from `reference`, on the worker host.
  max_pairs:
    type: int
    default: 2000000
    doc: Analyse only the first N pairs of each sample; 0 = all (hours per large sample).
  threads:
    type: int
    default: 8
  ram_mb:
    type: int
    default: 16000

steps:
  pair:
    run: ../esviritu/pair-samples.cwl
    in:
      run_path: run_path
      project_name: project_name
      include_undetermined: include_undetermined
    out: [samples, r1, r2, run_dir, project]

  measure:
    run: sortmerna.cwl
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
      reference: reference
      index_dir: index_dir
      max_pairs: max_pairs
      threads: threads
      ram_mb: ram_mb
    out: [summary, aligned_log, log]

outputs:
  samples:
    type: string[]
    outputSource: pair/samples
  summaries:
    type: File[]
    outputSource: measure/summary
  aligned_logs:
    type: File[]
    outputSource: measure/aligned_log
  logs:
    type: File[]
    outputSource: measure/log
