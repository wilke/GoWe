cwlVersion: v1.2
class: CommandLineTool

label: viral-assembly

doc: >-
  Viral Assembly — Assemble viral genomes from reads using IRMA or
  reference-guided strategies. Supports influenza, CoV, RSV, and Ebola.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: ViralAssembly
    executor: bvbrc

baseCommand: [ViralAssembly]

inputs:
  paired_end_lib:
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

  single_end_lib:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: string
            doc: "Read file in FASTQ, FASTA, or H5 [bvbrc:wsid]"
    doc: "Single end reads [bvbrc:group]"

  sra_id:
    type: string?
    doc: "SRA run accession (e.g. SRR12345678)"

  srr_id:
    type: string?
    doc: "SRA run accession (alternate field)"

  strategy:
    type: string?
    doc: "Assembly strategy [enum: auto, irma, reference_guided] [bvbrc:enum]"
    default: "auto"

  module:
    type: string?
    doc: "Virus module [enum: FLU, FLU-alt, FLU-avian, FLU-avian-residual, FLU-fast, FLU-lowQC, FLU-minion, FLU-pacbio, FLU-pgm, FLU-roche, FLU-secondary, FLU-sensitive, FLU-utr, CoV, RSV, EBOLA, FLU_AD] [bvbrc:enum]"

  reference_type:
    type: string?
    doc: "Reference type for reference-guided assembly [enum: genome, auto, genbank, fasta] [bvbrc:enum]"

  reference_genome_id:
    type: string?
    doc: "BV-BRC genome ID for reference-guided assembly"

  reference_genbank_accession:
    type: string?
    doc: "GenBank accession(s) for reference-guided assembly (use ACC1;ACC2 for multiple)"

  reference_fasta_file:
    type: string?
    doc: "Workspace path to reference FASTA (use path1;path2 for multiple files)"

  email:
    type: string?
    doc: "Email for NCBI Entrez (used when fetching GenBank accessions)"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  consensus_fasta:
    type: File?
    doc: "Consensus genome sequence (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  assembly_report:
    type: File?
    doc: "Assembly report"
    outputBinding:
      glob: "$(inputs.output_file)_report.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
