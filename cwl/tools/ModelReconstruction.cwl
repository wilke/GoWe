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
    type: File
    doc: "Input annotated genome for model reconstruction [bvbrc:wstype]"
  media:
    type: File?
    doc: "Media formulation in which model should be initially gapfilled [bvbrc:wstype]"
  template_model:
    type: File?
    doc: "Template upon which model should be constructed [bvbrc:wstype]"
  fulldb:
    type: boolean
    doc: "Add all reactions from template to model regardless of annotation [bvbrc:bool]"
    default: false
  output_path:
    type: Directory?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
