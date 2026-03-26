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

outputs: []
