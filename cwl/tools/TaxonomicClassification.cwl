cwlVersion: v1.2
class: CommandLineTool

doc: "classify reads — Compute taxonomic classification for read data"

hints:
  goweHint:
    bvbrc_app_id: TaxonomicClassification
    executor: bvbrc

baseCommand: [TaxonomicClassification]

inputs:
  input_type:
    type: string
    doc: "Input type (reads / contigs)"
  contigs:
    type: string?
    doc: "Input set of DNA contigs for classification"
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  save_classified_sequences:
    type: boolean?
    doc: "Save the classified sequences"
    default: false
  save_unclassified_sequences:
    type: boolean?
    doc: "Save the unclassified sequences"
    default: false
  algorithm:
    type: string
    doc: "Classification algorithm"
    default: "Kraken2"
  database:
    type: string
    doc: "Target database"
    default: "Kraken2"
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
