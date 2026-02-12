cwlVersion: v1.2
class: CommandLineTool

doc: "Comprehensive Genome Analysis — Analyze a genome from reads or contigs, generating a detailed analysis report."

hints:
  goweHint:
    bvbrc_app_id: ComprehensiveGenomeAnalysis
    executor: bvbrc

baseCommand: [ComprehensiveGenomeAnalysis]

inputs:
  input_type:
    type: string
    doc: "Input type (reads / contigs / genbank)"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  reference_assembly:
    type: string?
    doc: "Reference set of assembled DNA contigs"
  gto:
    type: string?
    doc: "Preannotated genome object"
  recipe:
    type: string?
    doc: "Recipe used for assembly"
    default: "auto"
  racon_iter:
    type: int?
    doc: "Racon polishing iterations (for long reads)"
    default: 2
  pilon_iter:
    type: int?
    doc: "Pilon polishing iterations (for short reads)"
    default: 2
  trim:
    type: boolean?
    doc: "Trim reads before assembly"
    default: false
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
  genome_size:
    type: string?
    doc: "Estimated genome size (for canu)"
    default: "5M"
  genbank_file:
    type: string?
    doc: "Genome to process"
  contigs:
    type: string?
    doc: "Input set of DNA contigs for annotation"
  scientific_name:
    type: string
    doc: "Scientific name of genome to be annotated"
  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy identfier for this genome"
  code:
    type: string
    doc: "Genetic code used in translation of DNA sequences"
    default: 11
  domain:
    type: string
    doc: "Domain of the submitted genome"
    default: "Bacteria"
  public:
    type: boolean?
    doc: "Make this genome public"
    default: false
  queue_nowait:
    type: boolean?
    doc: "If set, don't wait for the indexing to finish before marking the job complete."
    default: false
  skip_indexing:
    type: boolean?
    doc: "If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site."
    default: false
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  _parent_job:
    type: string?
    doc: "(Internal) Parent job for this annotation"
  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)."
  analyze_quality:
    type: boolean?
    doc: "If enabled, run quality analysis on genome"
  debug_level:
    type: int?
    doc: "Debugging level."
    default: 0

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
