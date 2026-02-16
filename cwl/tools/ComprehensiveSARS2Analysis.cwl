cwlVersion: v1.2
class: CommandLineTool

doc: "Comprehensive SARS2 Analysis — Analyze a genome from reads or contigs, generating a detailed analysis report."

hints:
  goweHint:
    bvbrc_app_id: ComprehensiveSARS2Analysis
    executor: bvbrc

baseCommand: [ComprehensiveSARS2Analysis]

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
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs"
  primers:
    type: string
    doc: "Primer set [enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long]"
    default: "ARTIC"
  primer_version:
    type: string?
    doc: "Primer version"
  recipe:
    type: string?
    doc: "Recipe used for assembly [enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore] [bvbrc:enum]"
    default: "auto"
  min_depth:
    type: int?
    doc: "Minimum coverage to add reads to consensus sequence"
    default: 100
  keep_intermediates:
    type: int?
    doc: "Keep all intermediate output from the pipeline"
    default: 0
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
    type: int
    doc: "NCBI Taxonomy identfier for this genome"
  code:
    type: string
    doc: "Genetic code used in translation of DNA sequences [bvbrc:enum]"
    default: 1
  domain:
    type: string
    doc: "Domain of the submitted genome [enum: Bacteria, Archaea, Viruses] [bvbrc:enum]"
    default: "Viruses"
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
  reference_virus_name:
    type: string?
    doc: "Reference virus name"
  container_id:
    type: string?
    doc: "(Internal) Container to use for this run"
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
