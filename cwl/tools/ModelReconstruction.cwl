cwlVersion: v1.2
class: CommandLineTool

doc: "Reconstruct metabolic model — Reconstructs a metabolic model from an annotated genome."

hints:
  goweHint:
    bvbrc_app_id: ModelReconstruction
    executor: bvbrc

baseCommand: [ModelReconstruction]

inputs:
  genome:
    type: string
    doc: "Input annotated genome for model reconstruction"
  media:
    type: string?
    doc: "Media formulation in which model should be initially gapfilled"
  template_model:
    type: string?
    doc: "Template upon which model should be constructed"
  fulldb:
    type: boolean
    doc: "Add all reactions from template to model regardless of annotation"
    default: false
  output_path:
    type: Directory?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
