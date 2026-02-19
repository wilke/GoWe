cwlVersion: v1.2
class: CommandLineTool
doc: "Sort lines using the `sort` command (Ubuntu container)"

requirements:
  DockerRequirement:
    dockerPull: ubuntu:22.04

baseCommand: sort

inputs:
  reverse:
    type: boolean
    inputBinding:
      position: 1
      prefix: "-r"
  input:
    type: File
    inputBinding:
      position: 2

outputs:
  output:
    type: File
    outputBinding:
      glob: output.txt

stdout: output.txt
