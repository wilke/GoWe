cwlVersion: v1.2
class: CommandLineTool
label: Check if file exists
doc: Checks if the input file exists and writes true/false to result file.

baseCommand: [sh, -c]

inputs:
  file_to_check:
    type: File

arguments:
  - valueFrom: |
      if [ -f "$(inputs.file_to_check.path)" ]; then
        echo "true" > exists_result.txt
      else
        echo "false" > exists_result.txt
      fi
    position: 1

outputs:
  result:
    type: File
    outputBinding:
      glob: exists_result.txt
  exists:
    type: boolean
    outputBinding:
      glob: exists_result.txt
      loadContents: true
      outputEval: $(self[0].contents.trim() === 'true')
