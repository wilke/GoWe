cwlVersion: v1.2
class: CommandLineTool
label: Simple echo test

hints:
  DockerRequirement:
    dockerPull: "alpine:latest"

baseCommand: ["echo"]

inputs:
  message:
    type: string
    default: "hello from GoWe"
    inputBinding:
      position: 1

stdout: output.txt

outputs:
  result:
    type: File
    outputBinding:
      glob: output.txt
