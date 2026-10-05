cwlVersion: v1.2
class: CommandLineTool

label: variation-analysis

doc: >-
  Identify SNVs — Identify and annotate small nucleotide variations
  relative to a reference genome using short read mapping and variant calling.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: Variation
    executor: bvbrc

baseCommand: [Variation]

inputs:
  reference_genome_id:
    type: string
    doc: "Reference genome ID"

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
    doc: "Single end read libraries [bvbrc:group]"

  srr_ids:
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs [bvbrc:array]"

  mapper:
    type: string?
    doc: "Short read mapper [enum: BWA-mem, BWA-mem-strict, Bowtie2, MOSAIK, LAST, minimap2, Snippy] [bvbrc:enum]"
    default: "BWA-mem"

  caller:
    type: string?
    doc: "SNP caller [enum: FreeBayes, BCFtools, Snippy] [bvbrc:enum]"
    default: "FreeBayes"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  variants_vcf:
    type: File?
    doc: "Called variants (VCF)"
    outputBinding:
      glob: "$(inputs.output_file).vcf.gz"

  alignment_bam:
    type: File?
    doc: "Read alignment to reference (BAM)"
    outputBinding:
      glob: "$(inputs.output_file).bam"

  consensus_fasta:
    type: File?
    doc: "Consensus sequence (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  variants_tsv:
    type: File?
    doc: "Annotated variants table (TSV)"
    outputBinding:
      glob: "$(inputs.output_file).variants.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
