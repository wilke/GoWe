cwlVersion: v1.2
class: CommandLineTool

doc: "Multiple sequence alignment variation service — Compute the multiple sequence alignment and analyze SNP/variance."

hints:
  goweHint:
    bvbrc_app_id: MSA
    executor: bvbrc

baseCommand: [MSA]

inputs:
  input_status:
    type: string?
    doc: "Input status [enum: unaligned, aligned] [bvbrc:enum]"
  input_type:
    type: string?
    doc: "Input type [enum: input_group, input_fasta, input_sequence, input_genomegroup, input_featurelist, input_genomelist] [bvbrc:enum]"
  fasta_files:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: fasta_file
          fields:
            - name: file
              type: File
              doc: "FASTA sequence file [bvbrc:wstype]"
            - name: type
              type: string?
              doc: "File type [enum: feature_dna_fasta, feature_protein_fasta] [bvbrc:enum]"
    doc: " [bvbrc:group]"
  select_genomegroup:
    type: File[]?
    doc: "Genome groups [bvbrc:wstype]"
  feature_groups:
    type: File[]?
    doc: "Feature groups [bvbrc:wstype]"
  feature_list:
    type: string[]?
    doc: "Feature list [bvbrc:list]"
  genome_list:
    type: string[]?
    doc: "Genome list [bvbrc:list]"
  aligner:
    type: string?
    doc: "Tool used for aligning multiple sequences to each other [enum: Muscle, Mafft, progressiveMauve] [bvbrc:enum]"
    default: "Muscle"
  alphabet:
    type: string
    doc: "Determines which sequence alphabet is present [enum: dna, protein] [bvbrc:enum]"
    default: "dna"
  fasta_keyboard_input:
    type: string?
    doc: "Text input for a fasta file"
  ref_type:
    type: string?
    doc: "Reference type [enum: none, string, feature_id, genome_id, first] [bvbrc:enum]"
    default: "none"
  strategy:
    type: string?
    doc: "Mafft strategy [enum: auto, fftns1, fftns2, fftnsi, einsi, linsi, ginsi] [bvbrc:enum]"
    default: "auto"
  ref_string:
    type: string?
    doc: "Reference sequence identity"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
