cwlVersion: v1.2
class: CommandLineTool
doc: "Reverse each line using the `rev` command (Ubuntu container)"

requirements:
  DockerRequirement:
    dockerPull: ubuntu:22.04

baseCommand: rev

inputs:
  input:
    type: File
    inputBinding:
      position: 1

outputs:
  output:
    type: File
    outputBinding:
      glob: output.txt

stdout: output.txt
