cwlVersion: v1.2
class: CommandLineTool

doc: "Transform expression data — Parses and transforms users differential expression data"

hints:
  goweHint:
    bvbrc_app_id: DifferentialExpression
    executor: bvbrc

baseCommand: [DifferentialExpression]

inputs:
  xfile:
    type: string
    doc: "Comparison values between samples"
  mfile:
    type: string?
    doc: "Metadata template filled out by the user"
  ustring:
    type: string
    doc: "User information (JSON string)"
  output_path:
    type: string?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
