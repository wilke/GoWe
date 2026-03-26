cwlVersion: v1.2
class: CommandLineTool

doc: "CEIRR Data Submission — Submit CEIRR data in CSV format"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: CEIRRDataSubmission
    executor: bvbrc

baseCommand: [CEIRRDataSubmission]

inputs:
  ceirr_data:
    type: string[]
    doc: "CEIRR data file in CSV format [bvbrc:list]"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
