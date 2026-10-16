cwlVersion: v1.2
class: Workflow
id: kraken2-run
label: Kraken2 for every sample of a sequencing run folder
doc: |
  Pairs the FASTQs in `run_path` (same rules as esviritu-run, reusing
  ../esviritu/pair-samples.cwl) and classifies each sample with Kraken2.
  run_path is read in place on the worker, so it must be the real path under
  the worker's --extra-bind (not a host symlink).

requirements:
  ScatterFeatureRequirement: {}
  StepInputExpressionRequirement: {}
  InlineJavascriptRequirement: {}

inputs:
  run_path:
    type: string
    label: Run folder, absolute path on the worker host (two FASTQ files per sample)
  project_name:
    type: string?
    label: Project name (default is the run folder name)
  include_undetermined:
    type: boolean
    default: false
    label: Also classify Undetermined reads
  database:
    type: string
    label: Kraken2 database directory on the worker host
    doc: Absolute path of the Kraken2 database directory on the worker host (read in place).
  confidence:
    type: float
    default: 0.0
    label: Kraken2 confidence threshold (0-1)
  threads:
    type: int
    default: 8
    label: CPU threads per sample
  ram_mb:
    type: int
    default: 24000
    label: Memory per sample (MB); must exceed the database size

steps:
  pair:
    run: ../esviritu/pair-samples.cwl
    in:
      run_path: run_path
      project_name: project_name
      include_undetermined: include_undetermined
    out: [samples, r1, r2, run_dir, project]

  classify:
    run: kraken2.cwl
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
      database: database
      confidence: confidence
      threads: threads
      ram_mb: ram_mb
    out: [report, log]

outputs:
  reports:
    type: File[]
    outputSource: classify/report
  logs:
    type: File[]
    outputSource: classify/log
  samples:
    type: string[]
    outputSource: pair/samples
