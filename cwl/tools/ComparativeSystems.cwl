cwlVersion: v1.2
class: CommandLineTool

doc: "Comparative Systems — Create datastructures to decompose genomes"

hints:
  goweHint:
    bvbrc_app_id: ComparativeSystems
    executor: bvbrc

baseCommand: [ComparativeSystems]

inputs:
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  genome_ids:
    type: string?
    doc: "Genome Ids"
  genome_groups:
    type: string?
    doc: "Genome Groups"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
