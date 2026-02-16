cwlVersion: v1.2
class: CommandLineTool

doc: "HA Subtype Numbering Conversion"

hints:
  goweHint:
    bvbrc_app_id: HASubtypeNumberingConversion
    executor: bvbrc

baseCommand: [HASubtypeNumberingConversion]

inputs:
  input_source:
    type: string
    doc: "Source of input [enum: feature_list, fasta_data, fasta_file, feature_group] [bvbrc:enum]"
  input_fasta_data:
    type: string?
    doc: "FASTA data"
  input_fasta_file:
    type: string?
    doc: "Workspace FASTA file [bvbrc:wsid]"
  input_feature_group:
    type: string?
    doc: "Feature group [bvbrc:wsid]"
  input_feature_list:
    type: string?
    doc: "Feature IDs"
  types:
    type: string
    doc: "Selected types in the submission"
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
