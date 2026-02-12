cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate genome for RAST — RAST worker app."

hints:
  goweHint:
    bvbrc_app_id: RASTJob
    executor: bvbrc

baseCommand: [RASTJob]

inputs:
  genome_object:
    type: string
    doc: "Input set of DNA contigs for annotation"
  output_path:
    type: string?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)."
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
