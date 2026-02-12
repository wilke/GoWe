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
    type: string?
  feature_groups:
    type: string?
    doc: "Feature groups"
  aligner:
    type: string?
    doc: "Tool used for aligning multiple sequences to each other."
    default: "Muscle"
  alphabet:
    type: string
    doc: "Determines which sequence alphabet is present."
    default: "dna"
  fasta_keyboard_input:
    type: string?
    doc: "Text input for a fasta file."
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
