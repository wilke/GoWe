#!/usr/bin/env cwl-runner
cwlVersion: v1.2
class: Workflow

doc: |
  Two-step pipeline for testing distributed worker execution.
  Step 1: Echo a message to a file.
  Step 2: Count characters in the file.

inputs:
  message:
    type: string
    doc: Message to echo

steps:
  echo:
    run:
      class: CommandLineTool
      baseCommand: echo
      inputs:
        msg:
          type: string
          inputBinding:
            position: 1
      outputs:
        out:
          type: stdout
      stdout: output.txt
    in:
      msg: message
    out: [out]

  count:
    run:
      class: CommandLineTool
      baseCommand: [wc, -c]
      inputs:
        file:
          type: File
          inputBinding:
            position: 1
      outputs:
        count:
          type: stdout
      stdout: count.txt
    in:
      file: echo/out
    out: [count]

outputs:
  result:
    type: File
    outputSource: count/count
