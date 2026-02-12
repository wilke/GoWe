cwlVersion: v1.2
class: CommandLineTool

doc: "Metadata-driven Comparative Analysis Tool (meta-CATS) — The meta-CATS tool looks for positions that significantly differ between user-defined groups of sequences."

hints:
  goweHint:
    bvbrc_app_id: MetaCATS
    executor: bvbrc

baseCommand: [MetaCATS]

inputs:
  alignment_file:
    type: string
    doc: "The location of the alignment file."
  group_file:
    type: string
    doc: "The location of a file that partitions sequences into groups."
  p_value:
    type: float
    doc: "The p-value cutoff for analyzing sequences."
    default: 0.05
  alignment_type:
    type: string
    doc: "The file format type."
  output_path:
    type: string
    doc: "Path to which the output will be written."
  output_file:
    type: string
    doc: "Basename for the generated output files."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
