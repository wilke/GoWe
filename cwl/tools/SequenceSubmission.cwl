cwlVersion: v1.2
class: CommandLineTool

doc: "Sequence Submission"

hints:
  goweHint:
    bvbrc_app_id: SequenceSubmission
    executor: bvbrc

baseCommand: [SequenceSubmission]

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
  metadata:
    type: string?
    doc: "Metadata as a workspace file of csv"
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
