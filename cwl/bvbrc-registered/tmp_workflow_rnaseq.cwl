cwlVersion: v1.2
class: CommandLineTool

label: rnaseq-analysis

doc: >-
  RNA-Seq Analysis — Align or assemble RNA-Seq reads into transcripts
  with normalized expression levels and differential expression.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: RNASeq
    executor: bvbrc

baseCommand: [RNASeq]

inputs:
  reference_genome_id:
    type: string
    doc: "Reference genome ID"

  genome_type:
    type: string
    doc: "Genome type [enum: bacteria, host] [bvbrc:enum]"

  recipe:
    type: string
    doc: "Analysis recipe [enum: HTSeq-DESeq, cufflinks, Host] [bvbrc:enum]"
    default: "HTSeq-DESeq"

  experimental_conditions:
    type: string[]?
    doc: "Experimental condition labels [bvbrc:array]"

  contrasts:
    type: string?
    doc: "Contrast list for differential expression"

  strand_specific:
    type: boolean?
    doc: "Are the reads in this study strand-specific?"
    default: true

  paired_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: paired_end_lib
          fields:
            - name: sample_id
              type: string
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: read1
              type: string
              doc: "Forward read file in FASTQ, FASTA, or BAM [bvbrc:wsid]"
            - name: read2
              type: string?
              doc: "Reverse read file in FASTQ, FASTA, or BAM [bvbrc:wsid]"
            - name: interleaved
              type: boolean?
              doc: "Are the paired end reads interleaved?"
              default: false
            - name: insert_size_mean
              type: int?
              doc: "Average insert size"
            - name: insert_size_stdev
              type: float?
              doc: "Average insert standard deviation"
            - name: condition
              type: int?
              doc: "Experimental condition index"
    doc: "Paired end read libraries [bvbrc:group]"

  single_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: single_end_lib
          fields:
            - name: sample_id
              type: string
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: read
              type: string
              doc: "Read file in FASTQ, FASTA, or BAM [bvbrc:wsid]"
            - name: condition
              type: int?
              doc: "Experimental condition index"
    doc: "Single end read libraries [bvbrc:group]"

  srr_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: srr_lib
          fields:
            - name: sample_id
              type: string
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: srr_accession
              type: string
              doc: "SRA Run accession with SRR prefix"
            - name: condition
              type: int?
              doc: "Experimental condition index"
    doc: "SRA read libraries [bvbrc:group]"

  host_ftp:
    type: string?
    doc: "Host FTP prefix for obtaining reference files"

  trimming:
    type: boolean?
    doc: "Run TrimGalore on the reads"
    default: false

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  gene_counts:
    type: File?
    doc: "Gene expression counts table"
    outputBinding:
      glob: "$(inputs.output_file)_gene_counts.tsv"

  diff_expression:
    type: File?
    doc: "Differential expression results"
    outputBinding:
      glob: "$(inputs.output_file)_diff_expression.tsv"

  mapping_report:
    type: File?
    doc: "Read alignment statistics report"
    outputBinding:
      glob: "$(inputs.output_file)_mapping_report.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
