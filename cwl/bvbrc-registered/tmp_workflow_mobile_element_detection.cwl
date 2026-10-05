cwlVersion: v1.2
class: CommandLineTool

label: mobile-element-detection

doc: >-
  Mobile Element Detection — Executes genome assembly (if needed) followed
  by geNomad pipeline for plasmid and virus identification, with automated
  annotation of viral genomes.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: MobileElementDetection
    executor: bvbrc

baseCommand: [MobileElementDetection]

inputs:
  input_type:
    type: string
    doc: "Type of input data [enum: contigs, reads] [bvbrc:enum]"
    default: "contigs"

  input_file:
    type: string?
    doc: "Input FASTA file of contigs (required when input_type is contigs) [bvbrc:wsid]"

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
    doc: "Paired end read libraries (used when input_type is reads) [bvbrc:group]"

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
              doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore] [bvbrc:enum]"
              default: "infer"
    doc: "Single end read libraries (used when input_type is reads) [bvbrc:group]"

  srr_ids:
    type: string[]?
    doc: "SRA run accessions (used when input_type is reads) [bvbrc:array]"

  recipe:
    type: string?
    doc: "Assembly recipe [enum: auto, unicycler, flye, meta-flye, canu, spades, meta-spades, plasmid-spades, single-cell, megahit] [bvbrc:enum]"
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

  target_depth:
    type: int?
    doc: "Target depth for BBNorm and Filtlong"
    default: 200

  normalize:
    type: boolean?
    doc: "Normalize reads using BBNorm before assembly"
    default: false

  filtlong:
    type: boolean?
    doc: "Filter long reads on length and quality to target depth"
    default: false

  genome_size:
    type: int?
    doc: "Estimated genome size (used for canu, flye, and filtlong)"
    default: 5000000

  min_contig_len:
    type: int?
    doc: "Minimal output contig length"
    default: 300

  min_contig_cov:
    type: float?
    doc: "Minimal output contig coverage"
    default: 5

  max_bases:
    type: int?
    doc: "Maximum bases across all input read files triggering downsampling"
    default: 10000000000

  filtering-preset:
    type: string?
    doc: "Filtering preset [enum: conservative, relaxed] [bvbrc:enum]"

  composition:
    type: string?
    doc: "Sample composition method [enum: auto, metagenome, virome] [bvbrc:enum]"
    default: "auto"

  force-auto:
    type: boolean?
    doc: "Force automatic composition estimation regardless of sample size"
    default: false

  lenient-taxonomy:
    type: boolean?
    doc: "Allow classification of virus genomes to taxa below the family rank"
    default: false

  full-ictv-lineage:
    type: boolean?
    doc: "Output the full ICTV lineage of each virus genome"
    default: true

  cleanup:
    type: boolean?
    doc: "Delete intermediate files after execution"
    default: true

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  plasmid_summary:
    type: File?
    doc: "Plasmid classification summary (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_plasmid_summary.tsv"

  virus_summary:
    type: File?
    doc: "Virus classification summary (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_virus_summary.tsv"

  genomad_scores:
    type: File?
    doc: "geNomad classification scores"
    outputBinding:
      glob: "$(inputs.output_file)_aggregated_classification.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
