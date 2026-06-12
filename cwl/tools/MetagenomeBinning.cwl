cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate metagenome data — Assemble, bin, and annotate metagenomic sample data"

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
      - type: array
        items:
          type: record
          name: paired_end_lib
          fields:
            - name: read1
              type: File
              doc: "Forward reads"
            - name: read2
              type: File?
              doc: "Reverse reads"
            - name: platform
              type: string?
              doc: "Sequencing platform"
              default: "infer"
            - name: interleaved
              type: boolean
              default: false
            - name: read_orientation_outward
              type: boolean
              default: false
    doc: " [bvbrc:group]"
  single_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: single_end_lib
          fields:
            - name: read
              type: File
              doc: "Read file"
            - name: platform
              type: string?
              doc: "Sequencing platform"
              default: "infer"
    doc: " [bvbrc:group]"
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  contigs:
    type: File?
    doc: "Input set of DNA contigs for annotation [bvbrc:wstype]"
  genome_group:
    type: string?
    doc: "Name of genome group into whcih the generated genome ids will be placed. "
  skip_indexing:
    type: boolean?
    doc: "If set, don't index the generated bins solr. They will not be available for analysis through the PATRIC site. [bvbrc:bool]"
    default: false
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe for annotation of bins"
  viral_recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe for annotation of viral bins"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  force_local_assembly:
    type: boolean
    doc: "If set, disable the use of remote clusters for assembly [bvbrc:bool]"
    default: false
  force_inline_annotation:
    type: boolean?
    doc: "If set, disable the use of the cluster for annotation [bvbrc:bool]"
    default: true
  perform_bacterial_binning:
    type: boolean?
    doc: "If set, perform bacterial binning [bvbrc:bool]"
    default: true
  perform_viral_binning:
    type: boolean?
    doc: "If set, perform viral binning of any contings unbinned after bacterial binning [bvbrc:bool]"
    default: false
  perform_viral_annotation:
    type: boolean?
    doc: "If set, perform viral annotation and loading of viral genomes into PATRIC database of any binned viruses [bvbrc:bool]"
    default: false
  perform_bacterial_annotation:
    type: boolean?
    doc: "If set, perform bacterial annotation and loading of bacterial genomes into PATRIC database of any binned bacterial genomes [bvbrc:bool]"
    default: true
  assembler:
    type: string?
    doc: "If set, use the given assembler"
  danglen:
    type: string?
    doc: "DNA kmer size for last-chance contig binning. Set to 0 to disable this step"
    default: "50"
  min_contig_len:
    type: int?
    doc: "Filter out short contigs"
    default: 400
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 4

outputs:
  contigs:
    type: File
    doc: "Assembled contigs (FASTA)"
    outputBinding:
      glob: "contigs.fasta"
  bins_json:
    type: File
    doc: "Binning results metadata (JSON)"
    outputBinding:
      glob: "bins.json"
  binning_report:
    type: File
    doc: "Binning quality report (HTML)"
    outputBinding:
      glob: "BinningReport.html"
  unbinned:
    type: File?
    doc: "Contigs not assigned to bins (FASTA)"
    outputBinding:
      glob: "unbinned.fasta"
  coverage_stats:
    type: File?
    doc: "Contig coverage statistics"
    outputBinding:
      glob: "coverage.stats.txt"
  bins_stats:
    type: File?
    doc: "Per-bin quality metrics"
    outputBinding:
      glob: "bins.stats.txt"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
