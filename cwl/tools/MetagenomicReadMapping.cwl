cwlVersion: v1.2
class: CommandLineTool

doc: "Metagenomic read mapping — Map metagenomic reads to a defined gene set"

hints:
  goweHint:
    bvbrc_app_id: MetagenomicReadMapping
    executor: bvbrc

baseCommand: [MetagenomicReadMapping]

inputs:
  gene_set_type:
    type: string
    doc: "Gene set type (predefined list / fasta file / feature group )"
  gene_set_name:
    type: string?
    doc: "Predefined gene set name"
  gene_set_fasta:
    type: string?
    doc: "Protein data in FASTA format"
  gene_set_feature_group:
    type: string?
    doc: "Name of feature group that defines the gene set "
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
