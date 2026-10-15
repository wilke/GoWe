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
  project_name:
    type: string?
  include_undetermined:
    type: boolean
    default: false
  database:
    type: Directory
  confidence:
    type: float
    default: 0.0
  threads:
    type: int
    default: 8
  ram_mb:
    type: int
    default: 24000

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
