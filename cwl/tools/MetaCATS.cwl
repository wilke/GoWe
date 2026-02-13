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
    type: File
    doc: "The location of the alignment file. [bvbrc:wstype]"
  group_file:
    type: File
    doc: "The location of a file that partitions sequences into groups. [bvbrc:wstype]"
  p_value:
    type: float
    doc: "The p-value cutoff for analyzing sequences."
    default: 0.05
  alignment_type:
    type: string
    doc: "The file format type. [enum: aligned_dna_fasta, aligned_protein_fasta] [bvbrc:enum]"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
