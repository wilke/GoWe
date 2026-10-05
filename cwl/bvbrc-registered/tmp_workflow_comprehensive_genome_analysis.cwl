cwlVersion: v1.2
class: CommandLineTool

label: comprehensive-genome-analysis

doc: >-
  Comprehensive Genome Analysis — Analyze a genome from reads, contigs,
  or GenBank input. Performs assembly (if reads), annotation, phylogenetic
  placement, AMR detection, and generates a detailed analysis report.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: ComprehensiveGenomeAnalysis
    executor: bvbrc

baseCommand: [ComprehensiveGenomeAnalysis]

inputs:
  input_type:
    type: string
    doc: "Input type [enum: reads, contigs, genbank] [bvbrc:enum]"

  scientific_name:
    type: string
    doc: "Scientific name of genome to be annotated"

  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy identifier for this genome"

  domain:
    type: string
    doc: "Domain of the submitted genome [enum: Bacteria, Archaea, Viruses, auto] [bvbrc:enum]"
    default: "auto"

  code:
    type: int
    doc: "Genetic code used in translation [enum: 0, 1, 4, 11, 25] [bvbrc:enum]"
    default: 11

  paired_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: paired_end_lib
          fields:
            - name: read1
              type: string?
              doc: "Forward read file in FASTQ/FASTA [bvbrc:wsid]"
            - name: read2
              type: string?
              doc: "Reverse read file in FASTQ/FASTA [bvbrc:wsid]"
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore] [bvbrc:enum]"
              default: "infer"
            - name: interleaved
              type: boolean?
              doc: "Are the paired end reads interleaved?"
              default: false
            - name: read_orientation_outward
              type: boolean?
              doc: "Do the two reads in each pair face outward?"
              default: false
            - name: insert_size_mean
              type: int?
              doc: "Average insert size"
            - name: insert_size_stdev
              type: float?
              doc: "Average insert standard deviation"
    doc: "Paired-end read libraries [bvbrc:group]"

  single_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: single_end_lib
          fields:
            - name: read
              type: string?
              doc: "Read file in FASTQ/FASTA/H5 [bvbrc:wsid]"
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore] [bvbrc:enum]"
              default: "infer"
    doc: "Single-end read libraries [bvbrc:group]"

  srr_ids:
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs"

  contigs:
    type: string?
    doc: "Input set of DNA contigs for annotation [bvbrc:wsid]"

  genbank_file:
    type: string?
    doc: "GenBank file to process [bvbrc:wsid]"

  gto:
    type: string?
    doc: "Preannotated genome object [bvbrc:wsid]"

  reference_assembly:
    type: string?
    doc: "Reference set of assembled DNA contigs [bvbrc:wsid]"

  recipe:
    type: string?
    doc: "Assembly recipe [enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell, flye] [bvbrc:enum]"
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
    doc: "Trim reads before assembly [bvbrc:bool]"
    default: false

  normalize:
    type: boolean?
    doc: "Normalize reads using BBNorm before assembly [bvbrc:bool]"
    default: false

  filtlong:
    type: boolean?
    doc: "Filter long reads on length and quality to target depth [bvbrc:bool]"
    default: false

  target_depth:
    type: int?
    doc: "Target depth for BBNorm and Filtlong"
    default: 200

  genome_size:
    type: int?
    doc: "Estimated genome size (used for canu, flye, and filtlong)"
    default: 5000000

  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300

  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5

  reference_genome_id:
    type: string?
    doc: "Reference genome ID"

  public:
    type: boolean?
    doc: "Make this genome public [bvbrc:bool]"
    default: false

  queue_nowait:
    type: boolean?
    doc: "Don't wait for indexing to finish before marking job complete [bvbrc:bool]"
    default: false

  skip_indexing:
    type: boolean?
    doc: "Don't index this genome in Solr [bvbrc:bool]"
    default: false

  analyze_quality:
    type: boolean?
    doc: "If enabled, run quality analysis on genome [bvbrc:bool]"

  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)"

  debug_level:
    type: int?
    doc: "Debugging level"
    default: 0

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  full_genome_report:
    type: File?
    doc: "Comprehensive genome analysis report (HTML)"
    outputBinding:
      glob: "$(inputs.output_file).html"

  genome_annotation:
    type: File?
    doc: "Annotated genome object"
    outputBinding:
      glob: "$(inputs.output_file).genome"

  assembly_contigs:
    type: File?
    doc: "Assembled contigs (if input was reads)"
    outputBinding:
      glob: "$(inputs.output_file)_contigs.fasta"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
