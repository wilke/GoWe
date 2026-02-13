cwlVersion: v1.2
class: CommandLineTool

doc: "Runs a ProbModelSEED job — Runs a ProbModelSEED modeling job"

hints:
  goweHint:
    bvbrc_app_id: RunProbModelSEEDJob
    executor: bvbrc

baseCommand: [RunProbModelSEEDJob]

inputs:
  command:
    type: string
    doc: "ProbModelSEED command to run"
  arguments:
    type: string
    doc: "ProbModelSEED arguments"
  output_path:
    type: Directory?
    doc: "Workspace folder for results (framework parameter) [bvbrc:folder]"
  output_file:
    type: string?
    doc: "Prefix for output file names (framework parameter)"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
