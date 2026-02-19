cwlVersion: v1.2
class: Workflow
doc: "Reverse the lines in a document, then sort those lines (Ubuntu container)"

requirements:
  DockerRequirement:
    dockerPull: ubuntu:22.04

inputs:
  input:
    type: File
    doc: "The input file to be processed."
  reverse_sort:
    type: boolean
    default: true
    doc: "If true, reverse (descending) sort"

outputs:
  output:
    type: File
    outputSource: sorted/output
    doc: "The output with the lines reversed and sorted."

steps:
  rev:
    run: revtool-docker.cwl
    in:
      input: input
    out: [output]

  sorted:
    run: sorttool-docker.cwl
    in:
      input: rev/output
      reverse: reverse_sort
    out: [output]
