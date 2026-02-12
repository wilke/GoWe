cwlVersion: v1.2
class: CommandLineTool

doc: "Subspecies Classification"

hints:
  goweHint:
    bvbrc_app_id: SubspeciesClassification
    executor: bvbrc

baseCommand: [SubspeciesClassification]

inputs:
  input_source:
    type: string
    doc: "Source of input (id_list, fasta_data, fasta_file, genome_group)"
  input_fasta_data:
    type: string?
    doc: "Input sequence in fasta formats"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of fasta data"
  input_genome_group:
    type: string?
    doc: "Input sequence as a workspace genome group"
  ref_msa_fasta:
    type: string?
    doc: "Reference multiple sequence alignment (Fasta-formatted)"
  virus_type:
    type: string
    doc: "Virus type"
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
