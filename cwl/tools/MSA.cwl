cwlVersion: v1.2
class: CommandLineTool

doc: "Multiple sequence alignment variation service — Compute the multiple sequence alignment and analyze SNP/variance."

hints:
  goweHint:
    bvbrc_app_id: MSA
    executor: bvbrc

baseCommand: [MSA]

inputs:
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
              doc: "FASTA sequence file"
            - name: type
              type: string?
              doc: "File type (feature_dna_fasta or feature_protein_fasta)"
    doc: " [bvbrc:group]"
  feature_groups:
    type: File?
    doc: "Feature groups [bvbrc:wstype]"
  aligner:
    type: string?
    doc: "Tool used for aligning multiple sequences to each other. [enum: Muscle] [bvbrc:enum]"
    default: "Muscle"
  alphabet:
    type: string
    doc: "Determines which sequence alphabet is present. [enum: dna, protein] [bvbrc:enum]"
    default: "dna"
  fasta_keyboard_input:
    type: string?
    doc: "Text input for a fasta file."
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
