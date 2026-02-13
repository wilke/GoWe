cwlVersion: v1.2
class: CommandLineTool

doc: "Date — Returns the current date and time."

hints:
  goweHint:
    bvbrc_app_id: Date
    executor: bvbrc

baseCommand: [Date]

inputs:
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
