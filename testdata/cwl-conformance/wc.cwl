cwlVersion: v1.2
class: CommandLineTool
baseCommand: wc

inputs:
  input_file:
    type: File
    inputBinding:
      position: 1
  lines_only:
    type: boolean?
    inputBinding:
      position: 0
      prefix: -l

outputs:
  output:
    type: stdout

stdout: counts.txt
