cwlVersion: v1.2
class: CommandLineTool

doc: "Sleep — Sleep a bit."

hints:
  goweHint:
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
    doc: "Workspace folder for results (framework parameter)"
  output_file:
    type: string
    doc: "Prefix for output file names"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
