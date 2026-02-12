cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate metagenome data — Assemble, bin, and annotate metagenomic sample data"

hints:
  goweHint:
    bvbrc_app_id: MetagenomeBinning
    executor: bvbrc

baseCommand: [MetagenomeBinning]

inputs:
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  contigs:
    type: string?
    doc: "Input set of DNA contigs for annotation"
  genome_group:
    type: string?
    doc: "Name of genome group into whcih the generated genome ids will be placed. "
  skip_indexing:
    type: boolean?
    doc: "If set, don't index the generated bins solr. They will not be available for analysis through the PATRIC site."
    default: false
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe for annotation of bins"
  viral_recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe for annotation of viral bins"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  force_local_assembly:
    type: boolean
    doc: "If set, disable the use of remote clusters for assembly"
    default: false
  force_inline_annotation:
    type: boolean?
    doc: "If set, disable the use of the cluster for annotation"
    default: true
  perform_bacterial_binning:
    type: boolean?
    doc: "If set, perform bacterial binning"
    default: true
  perform_viral_binning:
    type: boolean?
    doc: "If set, perform viral binning of any contings unbinned after bacterial binning"
    default: false
  perform_viral_annotation:
    type: boolean?
    doc: "If set, perform viral annotation and loading of viral genomes into PATRIC database of any binned viruses"
    default: false
  perform_bacterial_annotation:
    type: boolean?
    doc: "If set, perform bacterial annotation and loading of bacterial genomes into PATRIC database of any binned bacterial genomes"
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
  result:
    type: Directory
    outputBinding:
      glob: "."
