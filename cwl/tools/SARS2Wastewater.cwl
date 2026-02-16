cwlVersion: v1.2
class: CommandLineTool

doc: "SARS-CoV-2 Wastewater Surveillance — Freyja lineage deconvolution from wastewater samples"

hints:
  goweHint:
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
              doc: "Sample ID"
            - name: read1
              type: File
              doc: "Forward reads"
            - name: read2
              type: File?
              doc: "Reverse reads"
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, nanopore, iontorrent] [bvbrc:enum]"
              default: "infer"
            - name: interleaved
              type: boolean
              default: false
            - name: read_orientation_outward
              type: boolean
              default: false
            - name: primers
              type: string?
              doc: "Primers"
            - name: primer_version
              type: string?
              doc: "Primer version"
            - name: sample_level_date
              type: string?
              doc: "Sample-level date"
    doc: " [bvbrc:group]"
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
              doc: "Sample ID"
            - name: read
              type: File
              doc: "Read file"
            - name: platform
              type: string?
              doc: "Sequencing platform"
            - name: primers
              type: string?
              doc: "Primers"
            - name: primer_version
              type: string?
              doc: "Primer version"
            - name: sample_level_date
              type: string?
              doc: "Sample-level date"
    doc: " [bvbrc:group]"
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
              doc: "Sample ID"
            - name: srr_accession
              type: string
              doc: "SRA run accession"
            - name: primers
              type: string?
              doc: "Primers"
            - name: primer_version
              type: string?
              doc: "Primer version"
            - name: sample_level_date
              type: string?
              doc: "Sample-level date"
    doc: " [bvbrc:group]"
  recipe:
    type: string?
    doc: "Recipe [enum: onecodex] [bvbrc:enum]"
    default: "auto"
  primers:
    type: string
    doc: "Primers [enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long] [bvbrc:enum]"
    default: "ARTIC"
  minimum_base_quality_score:
    type: int?
    doc: "Freyja --minq"
    default: 20
  minimum_genome_coverage:
    type: int?
    doc: "Freyja --mincov"
    default: 60
  agg_minimum_lineage_abundance:
    type: float?
    doc: "Freyja --thresh"
    default: 0.01
  minimum_coverage_depth:
    type: int?
    doc: "Freyja --depthcutoff"
    default: 0
  confirmedonly:
    type: boolean?
    doc: "Exclude unconfirmed lineages [bvbrc:bool]"
    default: false
  minimum_lineage_abundance:
    type: float?
    doc: "Freyja --eps"
    default: 0.001
  coverage_estimate:
    type: int?
    doc: "10x coverage estimate"
    default: 10
  timeseries_plot_interval:
    type: string?
    doc: "Timeseries interval (MS or D)"
    default: "0"
  primer_version:
    type: string?
    doc: "Primer version"
  barcode_csv:
    type: string?
    doc: "Custom barcodes for demix"
  sample_metadata_csv:
    type: string?
    doc: "CSV with fastq to sampling date mapping"
    default: "0"
  keep_intermediates:
    type: boolean?
    doc: "Keep intermediates [bvbrc:bool]"
    default: true
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
