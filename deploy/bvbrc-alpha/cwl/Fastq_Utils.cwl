cwlVersion: v1.2
class: CommandLineTool

label: fastq-utilities

doc: >-
  FASTQ Utilities — Common processing of FASTQ files including trimming,
  quality filtering, alignment, and SRA download.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: FastqUtils
    executor: bvbrc

baseCommand: [FastqUtils]

inputs:
  recipe:
    type: string[]
    default: ["trim", "fastqc"]
    doc: "[bvbrc:array] FastqUtils recipe commands available: trim, fastqc, align, paired_filter, scrub_human"

  reference_genome_id:
    type: string?
    doc: "Reference genome ID"

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
          - name: platform
            type: string?
            doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore] [bvbrc:enum]"
            default: "infer"
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
          - name: platform
            type: string?
            doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore] [bvbrc:enum]"
            default: "infer"
    doc: "Single end reads [bvbrc:group]"

  srr_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: srr_lib
          fields:
            - name: srr_accession
              type: string
              doc: "SRA Run accession with SRR prefix"
    doc: "SRA read libraries [bvbrc:group]"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  processed_reads:
    type: File?
    doc: "Processed FASTQ output"
    outputBinding:
      glob: "$(inputs.output_file).fastq.gz"

  quality_report:
    type: File?
    doc: "Quality assessment report"
    outputBinding:
      glob: "$(inputs.output_file)_report.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
