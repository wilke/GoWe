cwlVersion: v1.2
class: CommandLineTool

doc: "Compute synteny graph — Computes a synteny graph"

hints:
  goweHint:
    bvbrc_app_id: SyntenyGraph
    executor: bvbrc

baseCommand: [SyntenyGraph]

inputs:
  output_path:
    type: Directory
    doc: "Path to which the output will be written. "
  output_file:
    type: string
    doc: "Basename for the generated output files."
  genome_ids:
    type: string
    doc: "Input genomes"
  ksize:
    type: int
    doc: "Minimum neighborhood size for alignment"
    default: 3
  context:
    type: string
    doc: "Context of alignment"
    default: "genome"
  diversity:
    type: string
    doc: "Diversity quotient calculation"
    default: "species"
  alpha:
    type: string
    doc: "Alphabet to use to group genes"
    default: "patric_pgfam"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
