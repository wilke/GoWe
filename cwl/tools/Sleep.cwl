cwlVersion: v1.2
class: CommandLineTool

doc: "Sleep — Sleep a bit."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: Sleep
    executor: bvbrc

baseCommand: [Sleep]

inputs:
  sleep_time:
    type: int?
    doc: "Time to sleep, in seconds."
    default: 10
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
