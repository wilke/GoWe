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
    doc: "Source of input (id_list, fasta_data, fasta_file, genome_group) [enum: id_list, fasta_data, fasta_file, genome_group] [bvbrc:enum]"
  input_fasta_data:
    type: string?
    doc: "Input sequence in fasta formats"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of fasta data [bvbrc:wsid]"
  input_genome_group:
    type: string?
    doc: "Input sequence as a workspace genome group [bvbrc:wsid]"
  metadata:
    type: string
    doc: "Metadata CSV file [bvbrc:wsid]"
  affiliation:
    type: string?
    doc: "Submitter affiliation"
  first_name:
    type: string
    doc: "Submitter first name"
  last_name:
    type: string
    doc: "Submitter last name"
  email:
    type: string
    doc: "Submitter email"
  consortium:
    type: string?
    doc: "Consortium"
  country:
    type: string?
    doc: "Country"
  phoneNumber:
    type: string?
    doc: "Phone number"
  street:
    type: string?
    doc: "Street address"
  postal_code:
    type: string?
    doc: "Postal code"
  city:
    type: string?
    doc: "City"
  state:
    type: string?
    doc: "State"
  numberOfSequences:
    type: string?
    doc: "Number of sequences"
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
