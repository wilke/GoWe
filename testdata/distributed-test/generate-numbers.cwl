cwlVersion: v1.2
class: CommandLineTool
label: Generate numbers 1 to N
doc: Creates a file with numbers from 1 to the specified count, one per line.

baseCommand: [sh, -c]

inputs:
  count:
    type: int
    default: 100

arguments:
  - valueFrom: "seq 1 $(inputs.count) > numbers.txt"
    position: 1

outputs:
  numbers_file:
    type: File
    outputBinding:
      glob: numbers.txt
