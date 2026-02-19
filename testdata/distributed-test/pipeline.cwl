cwlVersion: v1.2
class: Workflow
label: Distributed Test Pipeline
doc: |
  Three-step pipeline to test distributed execution with shared volumes:
  1. Generate a file with numbers 1-100
  2. Count lines using wc
  3. Verify the output file exists

inputs:
  line_count:
    type: int
    default: 100
    doc: Number of lines to generate (1 to N)

steps:
  generate:
    run: generate-numbers.cwl
    in:
      count: line_count
    out: [numbers_file]

  count:
    run: count-lines.cwl
    in:
      input_file: generate/numbers_file
    out: [count_file]

  verify:
    run: check-exists.cwl
    in:
      file_to_check: count/count_file
    out: [result, exists]

outputs:
  numbers:
    type: File
    outputSource: generate/numbers_file
    doc: File containing numbers 1-100

  line_count_result:
    type: File
    outputSource: count/count_file
    doc: File containing the line count from wc

  verification_result:
    type: File
    outputSource: verify/result
    doc: File containing true/false

  file_exists:
    type: boolean
    outputSource: verify/exists
    doc: Boolean indicating if the count file exists
