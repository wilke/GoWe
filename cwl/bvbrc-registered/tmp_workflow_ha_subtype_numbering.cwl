cwlVersion: v1.2
class: CommandLineTool

label: ha-subtype-numbering

doc: >-
  HA Subtype Numbering Conversion — Convert influenza hemagglutinin
  sequences to standardized subtype numbering.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: HASubtypeNumberingConversion
    executor: bvbrc

baseCommand: [HASubtypeNumberingConversion]

inputs:
  input_source:
    type: string
    doc: "Source of input [enum: feature_list, fasta_data, fasta_file, feature_group] [bvbrc:enum]"

  input_fasta_data:
    type: string?
    doc: "Input sequence in FASTA format"

  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace FASTA file [bvbrc:wsid]"

  input_feature_group:
    type: string?
    doc: "Input sequence as a workspace feature group [bvbrc:wsid]"

  input_feature_list:
    type: string?
    doc: "Input sequence as a list of feature IDs"

  types:
    type: string[]
    doc: "Selected HA subtypes for numbering conversion (array of subtype codes, e.g. ['H3','H5'])"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  numbering_results:
    type: File?
    doc: "HA subtype numbering conversion results"
    outputBinding:
      glob: "$(inputs.output_file)_numbering.tsv"

  alignment:
    type: File?
    doc: "Aligned sequences with subtype numbering"
    outputBinding:
      glob: "$(inputs.output_file)_alignment.fasta"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
