cwlVersion: v1.2
class: CommandLineTool

label: sars2-wastewater

doc: >-
  SARS-CoV-2 Wastewater Surveillance — Assemble SARS-CoV-2 reads from
  wastewater samples and perform Freyja lineage deconvolution for
  variant abundance estimation.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: SARS2Wastewater
    executor: bvbrc

baseCommand: [SARS2Wastewater]

inputs:
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
            - name: primers
              type: string
              doc: "Primer set for this sample"
              default: "blank primer"
            - name: primer_version
              type: string
              doc: "Primer version for this sample"
              default: "blank primer version"
            - name: sample_level_date
              type: string?
              doc: "Date of sample collection for time series analysis"
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
              doc: "Read file in FASTQ [bvbrc:wsid]"
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, nanopore, iontorrent] [bvbrc:enum]"
              default: "infer"
            - name: primers
              type: string
              doc: "Primer set for this sample"
              default: "blank primer"
            - name: primer_version
              type: string
              doc: "Primer version for this sample"
              default: "blank primer version"
            - name: sample_level_date
              type: string?
              doc: "Date of sample collection for time series analysis"
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
              doc: "SRA Run accession ID (e.g. SRR10971381)"
            - name: primers
              type: string
              doc: "Primer set for this sample"
              default: "blank primer"
            - name: primer_version
              type: string
              doc: "Primer version for this sample"
              default: "blank primer version"
            - name: sample_level_date
              type: string?
              doc: "Date of sample collection for time series analysis"
    doc: "SRA read libraries [bvbrc:group]"

  recipe:
    type: string?
    doc: "Assembly recipe [enum: onecodex] [bvbrc:enum]"
    default: "onecodex"

  primers:
    type: string
    doc: "Default primer set [enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long] [bvbrc:enum]"
    default: "ARTIC"

  primer_version:
    type: string?
    doc: "Version number for primer set"

  minimum_base_quality_score:
    type: int?
    doc: "Minimum base quality score (Freyja --minq)"
    default: 20

  minimum_genome_coverage:
    type: int?
    doc: "Minimum genome coverage (Freyja aggregate --mincov)"
    default: 60

  minimum_coverage_depth:
    type: int?
    doc: "Minimum coverage depth (Freyja demix --depthcutoff)"
    default: 0

  minimum_lineage_abundance:
    type: float?
    doc: "Minimum lineage abundance (Freyja demix --eps)"
    default: 0.001

  agg_minimum_lineage_abundance:
    type: float?
    doc: "Minimum lineage abundance for plot (Freyja plot --thresh)"
    default: 0.01

  coverage_estimate:
    type: int?
    doc: "Coverage cutoff for 10x estimate (Freyja demix --covcut)"
    default: 10

  confirmedonly:
    type: boolean?
    doc: "Exclude unconfirmed lineages from analysis"
    default: false

  timeseries_plot_interval:
    type: string?
    doc: "Timeseries plot interval (Freyja plot --interval: MS or D)"
    default: "0"

  barcode_csv:
    type: string?
    doc: "Custom barcode file path (Freyja demix --barcodes)"

  sample_metadata_csv:
    type: string?
    doc: "CSV mapping input FASTQ files to sampling dates"

  keep_intermediates:
    type: boolean?
    doc: "Keep all intermediate pipeline output"
    default: true

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  demix_results:
    type: File?
    doc: "Freyja lineage deconvolution results"
    outputBinding:
      glob: "$(inputs.output_file)_demix_results.tsv"

  aggregate_results:
    type: File?
    doc: "Freyja aggregated results across samples"
    outputBinding:
      glob: "$(inputs.output_file)_aggregate.tsv"

  lineage_plot:
    type: File?
    doc: "Lineage abundance plot"
    outputBinding:
      glob: "$(inputs.output_file)_plot.pdf"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
