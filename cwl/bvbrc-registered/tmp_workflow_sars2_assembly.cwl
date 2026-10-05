cwlVersion: v1.2
class: CommandLineTool

label: sars2-assembly

doc: >-
  SARS-CoV-2 Assembly — Assemble SARS-CoV-2 reads into a consensus
  sequence using primer-directed amplicon workflows.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: SARS2Assembly
    executor: bvbrc

baseCommand: [SARS2Assembly]

inputs:
  paired_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
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
              doc: "Sequencing platform [enum: infer, illumina, pacbio, nanopore, iontorrent] [bvbrc:enum]"
              default: "infer"
            - name: interleaved
              type: boolean?
              doc: "Are the paired end reads interleaved?"
              default: false
            - name: read_orientation_outward
              type: boolean?
              doc: "Do the two reads in each pair face outward?"
              default: false
    doc: "Paired end read libraries [bvbrc:group]"

  single_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: single_end_lib
          fields:
            - name: read
              type: string
              doc: "Read file in FASTQ [bvbrc:wsid]"
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, nanopore, iontorrent] [bvbrc:enum]"
              default: "infer"
    doc: "Single end read libraries [bvbrc:group]"

  srr_ids:
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs [bvbrc:array]"

  recipe:
    type: string?
    doc: "Assembly recipe [enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore] [bvbrc:enum]"
    default: "auto"

  primers:
    type: string
    doc: "Primer set for assembly [enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long] [bvbrc:enum]"
    default: "ARTIC"

  primer_version:
    type: string?
    doc: "Version number for primer set"

  min_depth:
    type: int?
    doc: "Minimum coverage depth to add reads to consensus"
    default: 100

  max_depth:
    type: int?
    doc: "Maximum read depth to consider for consensus"
    default: 8000

  keep_intermediates:
    type: int?
    doc: "Keep all intermediate pipeline output (1 = yes)"
    default: 0

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  consensus_fasta:
    type: File?
    doc: "Consensus sequence (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  variants_vcf:
    type: File?
    doc: "Called variants (VCF)"
    outputBinding:
      glob: "$(inputs.output_file).vcf"

  assembly_report:
    type: File?
    doc: "Assembly report"
    outputBinding:
      glob: "$(inputs.output_file)_report.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
