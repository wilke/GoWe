cwlVersion: v1.2
class: CommandLineTool

label: expression-import

doc: >-
  Expression Import — Parse and transform user-provided differential
  expression data for loading into BV-BRC.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: DifferentialExpression
    executor: bvbrc

baseCommand: [DifferentialExpression]

inputs:
  xfile:
    type: string
    doc: "Experiment data file with comparison values between samples [bvbrc:wsid]"

  mfile:
    type: string?
    doc: "Metadata template file filled out by the user [bvbrc:wsid]"

  ustring:
    type: string
    doc: "User information (JSON string)"

  output_path:
    type: string?
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string?
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  expression_data:
    type: File?
    doc: "Transformed expression data"
    outputBinding:
      glob: "$(inputs.output_file)_expression.json"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
