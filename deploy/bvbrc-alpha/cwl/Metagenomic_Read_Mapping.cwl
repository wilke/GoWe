cwlVersion: v1.2
class: CommandLineTool

label: metagenomic-read-mapping

doc: >-
  Metagenomic Read Mapping — Map metagenomic reads to a defined gene set
  (e.g. VFDB, CARD, or a custom feature group / FASTA file).

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: MetagenomicReadMapping
    executor: bvbrc

baseCommand: [MetagenomicReadMapping]

inputs:
  gene_set_type:
    type: string
    doc: "Gene set type [enum: predefined_list, fasta_file, feature_group] [bvbrc:enum]"

  gene_set_name:
    type: string?
    doc: "Predefined gene set name [enum: VFDB, CARD, feature_group, fasta_file] [bvbrc:enum]"

  gene_set_fasta:
    type: string?
    doc: "Protein data in FASTA format [bvbrc:wsid]"

  gene_set_feature_group:
    type: string?
    doc: "Name of feature group that defines the gene set"

  paired_end_libs:
    type:
      - "null"
      - type: record
        name: paired_end_lib
        fields:
          - name: read1
            type: string
            doc: "Forward read file in FASTQ [bvbrc:wsid]"
          - name: read2
            type: string?
            doc: "Reverse read file in FASTQ [bvbrc:wsid]"
    doc: "Paired end reads [bvbrc:group]"

  single_end_libs:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: string
            doc: "Read file in FASTQ [bvbrc:wsid]"
    doc: "Single end reads [bvbrc:group]"

  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  mapping_report:
    type: File?
    doc: "Read mapping results report"
    outputBinding:
      glob: "$(inputs.output_file)_mapping_report.tsv"

  gene_counts:
    type: File?
    doc: "Gene hit counts table"
    outputBinding:
      glob: "$(inputs.output_file)_gene_counts.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
