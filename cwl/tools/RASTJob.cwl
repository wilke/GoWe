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
    type: File
    doc: "Input set of DNA contigs for annotation [bvbrc:wstype]"
  output_path:
    type: Directory?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)."
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
