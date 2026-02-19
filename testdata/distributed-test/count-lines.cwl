cwlVersion: v1.2
class: CommandLineTool
label: Count lines in file
doc: Runs wc -l on input file and saves result to output file.

baseCommand: [sh, -c]

inputs:
  input_file:
    type: File

arguments:
  - valueFrom: "wc -l < $(inputs.input_file.path) > line_count.txt"
    position: 1

outputs:
  count_file:
    type: File
    outputBinding:
      glob: line_count.txt
