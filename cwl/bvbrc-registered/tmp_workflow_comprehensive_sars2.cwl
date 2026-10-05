cwlVersion: v1.2
class: CommandLineTool

label: comprehensive-sars2-analysis

doc: >-
  Comprehensive SARS-CoV-2 Analysis — Assemble, annotate, and analyze
  a SARS-CoV-2 genome from reads, contigs, or GenBank input.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: ComprehensiveSARS2Analysis
    executor: bvbrc

baseCommand: [ComprehensiveSARS2Analysis]

inputs:
  input_type:
    type: string
    doc: "Input type [enum: reads, contigs, genbank] [bvbrc:enum]"

  scientific_name:
    type: string
    doc: "Scientific name of genome to be annotated"

  taxonomy_id:
    type: int
    doc: "NCBI Taxonomy identifier for this genome"

  domain:
    type: string
    doc: "Domain of the submitted genome [enum: Bacteria, Archaea, Viruses] [bvbrc:enum]"
    default: "Viruses"

  code:
    type: int
    doc: "Genetic code used in translation [enum: 11, 4, 1] [bvbrc:enum]"
    default: 1

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
            - name: platform
              type: string?
              doc: "Sequencing platform [enum: infer, illumina, pacbio, nanopore, iontorrent] [bvbrc:enum]"
              default: "infer"
    doc: "Single end read libraries [bvbrc:group]"

  srr_ids:
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs [bvbrc:array]"

  contigs:
    type: string?
    doc: "Input set of DNA contigs for annotation [bvbrc:wsid]"

  genbank_file:
    type: string?
    doc: "GenBank file to process [bvbrc:wsid]"

  recipe:
    type: string?
    doc: "Assembly recipe [enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore] [bvbrc:enum]"
    default: "auto"

  primers:
    type: string
    doc: "Primer set for assembly [enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long] [bvbrc:enum]"
    default: "ARTIC"

  primer_version:
    type: string?
    doc: "Version number for primer set"

  min_depth:
    type: int?
    doc: "Minimum coverage depth to add reads to consensus"
    default: 100

  reference_genome_id:
    type: string?
    doc: "Reference genome ID"

  reference_virus_name:
    type: string?
    doc: "Reference virus name"

  public:
    type: boolean?
    doc: "Make this genome public"
    default: false

  skip_indexing:
    type: boolean?
    doc: "Don't index this genome in Solr"
    default: false

  queue_nowait:
    type: boolean?
    doc: "Don't wait for indexing to finish before marking job complete"
    default: false

  analyze_quality:
    type: boolean?
    doc: "Run quality analysis on genome"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  full_genome:
    type: File?
    doc: "Annotated genome (GenBank)"
    outputBinding:
      glob: "$(inputs.output_file).gbk"

  consensus_fasta:
    type: File?
    doc: "Consensus sequence (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  analysis_report:
    type: File?
    doc: "Comprehensive analysis report (HTML)"
    outputBinding:
      glob: "$(inputs.output_file)_report.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
