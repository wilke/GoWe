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
    type: File
    doc: "Comparison values between samples [bvbrc:wstype]"
  mfile:
    type: File?
    doc: "Metadata template filled out by the user [bvbrc:wstype]"
  ustring:
    type: string
    doc: "User information (JSON string)"
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
