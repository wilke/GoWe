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
    doc: "Input type (reads / contigs / genbank) [enum: reads, contigs, genbank] [bvbrc:enum]"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
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
  reference_assembly:
    type: File?
    doc: "Reference set of assembled DNA contigs [bvbrc:wstype]"
  gto:
    type: File?
    doc: "Preannotated genome object [bvbrc:wstype]"
  recipe:
    type: string?
    doc: "Recipe used for assembly [enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell] [bvbrc:enum]"
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
    type: File?
    doc: "Genome to process [bvbrc:wstype]"
  contigs:
    type: File?
    doc: "Input set of DNA contigs for annotation [bvbrc:wstype]"
  scientific_name:
    type: string
    doc: "Scientific name of genome to be annotated"
  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy identfier for this genome"
  code:
    type: string
    doc: "Genetic code used in translation of DNA sequences [enum: 11, 4] [bvbrc:enum]"
    default: 11
  domain:
    type: string
    doc: "Domain of the submitted genome [enum: Bacteria, Archaea] [bvbrc:enum]"
    default: "Bacteria"
  public:
    type: boolean?
    doc: "Make this genome public [bvbrc:bool]"
    default: false
  queue_nowait:
    type: boolean?
    doc: "If set, don't wait for the indexing to finish before marking the job complete. [bvbrc:bool]"
    default: false
  skip_indexing:
    type: boolean?
    doc: "If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. [bvbrc:bool]"
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
    doc: "If enabled, run quality analysis on genome [bvbrc:bool]"
  debug_level:
    type: int?
    doc: "Debugging level."
    default: 0

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
