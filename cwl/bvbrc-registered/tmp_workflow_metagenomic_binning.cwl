cwlVersion: v1.2
class: CommandLineTool

label: metagenomic-binning

doc: >-
  Metagenome Binning — Assemble, bin, and annotate metagenomic sample
  data into individual genome bins.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: MetagenomeBinning
    executor: bvbrc

baseCommand: [MetagenomeBinning]

inputs:
  paired_end_libs:
    type:
      - "null"
      - type: record
        name: paired_end_lib
        fields:
          - name: read1
            type: string
            doc: "Forward read file in FASTQ or FASTA [bvbrc:wsid]"
          - name: read2
            type: string?
            doc: "Reverse read file in FASTQ or FASTA [bvbrc:wsid]"
    doc: "Paired end reads [bvbrc:group]"

  single_end_libs:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: string
            doc: "Read file in FASTQ, FASTA, or H5 [bvbrc:wsid]"
    doc: "Single end reads [bvbrc:group]"

  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"

  contigs:
    type: string?
    doc: "Input set of DNA contigs for annotation [bvbrc:wsid]"

  genome_group:
    type: string?
    doc: "Output genome group name for generated genome IDs"

  skip_indexing:
    type: boolean?
    doc: "Don't index the generated bins in Solr"
    default: false

  recipe:
    type: string?
    doc: "Non-default annotation recipe for bacterial bins"

  viral_recipe:
    type: string?
    doc: "Non-default annotation recipe for viral bins"

  force_local_assembly:
    type: boolean?
    doc: "Disable the use of remote clusters for assembly"
    default: false

  force_inline_annotation:
    type: boolean?
    doc: "Disable the use of the cluster for annotation"
    default: true

  perform_bacterial_binning:
    type: boolean?
    doc: "Perform bacterial binning"
    default: true

  perform_viral_binning:
    type: boolean?
    doc: "Perform viral binning of contigs unbinned after bacterial binning"
    default: false

  perform_viral_annotation:
    type: boolean?
    doc: "Perform viral annotation and loading of viral genomes"
    default: false

  perform_bacterial_annotation:
    type: boolean?
    doc: "Perform bacterial annotation and loading of bacterial genomes"
    default: true

  assembler:
    type: string?
    doc: "Assembler to use"

  danglen:
    type: string?
    doc: "DNA kmer size for last-chance contig binning (set to 0 to disable)"
    default: "50"

  min_contig_len:
    type: int?
    doc: "Minimal output contig length"
    default: 400

  min_contig_cov:
    type: float?
    doc: "Minimal output contig coverage"
    default: 4

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  binning_report:
    type: File?
    doc: "Binning quality report (HTML)"
    outputBinding:
      glob: "$(inputs.output_file)_BinningReport.html"

  bin_summary:
    type: File?
    doc: "Summary of bins found"
    outputBinding:
      glob: "$(inputs.output_file)_bin_summary.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
