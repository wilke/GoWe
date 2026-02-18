#!/usr/bin/env cwl-runner
cwlVersion: v1.2
class: CommandLineTool

doc: |
  Simple tool that writes input to a file using shell redirection.
  This tests explicit file output collection (not stdout capture).

requirements:
  ShellCommandRequirement: {}

baseCommand: []

arguments:
  - shellQuote: false
    valueFrom: "echo"
  - valueFrom: $(inputs.message)
  - shellQuote: false
    valueFrom: "> output.txt"

inputs:
  message:
    type: string

outputs:
  result:
    type: File
    outputBinding:
      glob: "output.txt"
