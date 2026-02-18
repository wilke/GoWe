#!/usr/bin/env cwl-runner
cwlVersion: v1.2
class: CommandLineTool

doc: |
  Simple tool that creates a file using touch.
  This tests explicit file output collection without shell features.

baseCommand: [touch]

inputs:
  filename:
    type: string
    default: "output.txt"
    inputBinding:
      position: 1

outputs:
  result:
    type: File
    outputBinding:
      glob: "*.txt"
