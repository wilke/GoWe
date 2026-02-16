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
    doc: "Gene set type (predefined list / fasta file / feature group ) [enum: predefined_list, fasta_file, feature_group] [bvbrc:enum]"
  gene_set_name:
    type: string?
    doc: "Predefined gene set name [enum: MLST, CARD] [bvbrc:enum]"
  gene_set_fasta:
    type: File?
    doc: "Protein data in FASTA format [bvbrc:wstype]"
  gene_set_feature_group:
    type: string?
    doc: "Name of feature group that defines the gene set "
  paired_end_libs:
    type:
      - "null"
      - type: record
        name: paired_end_lib
        fields:
          - name: read1
            type: File
            doc: "Forward reads"
          - name: read2
            type: File?
            doc: "Reverse reads"
    doc: "Paired-end reads (singular)"
  single_end_libs:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: File
            doc: "Read file"
    doc: "Single-end reads (singular)"
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
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
