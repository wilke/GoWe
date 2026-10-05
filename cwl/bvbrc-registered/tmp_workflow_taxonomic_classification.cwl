cwlVersion: v1.2
class: CommandLineTool

label: taxonomic-classification

doc: >-
  Taxonomic Classification — Compute taxonomic classification for read
  data using Kraken2 against multiple database options.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: TaxonomicClassification
    executor: bvbrc

baseCommand: [TaxonomicClassification]

inputs:
  host_genome:
    type: string
    doc: "Host genome for filtering [enum: homo_sapiens, mus_musculus, rattus_norvegicus, caenorhabditis_elegans, drosophila_melanogaster_strain, danio_rerio_strain_tuebingen, gallus_gallus, macaca_mulatta, mustela_putorius_furo, sus_scrofa, no_host] [bvbrc:enum]"
    default: "no_host"

  analysis_type:
    type: string
    doc: "Workflow type [enum: pathogen, microbiome, 16S] [bvbrc:enum]"
    default: "16S"

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
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: read1
              type: string
              doc: "Forward read file in FASTQ or FASTA [bvbrc:wsid]"
            - name: read2
              type: string?
              doc: "Reverse read file in FASTQ or FASTA [bvbrc:wsid]"
    doc: "Paired end read libraries [bvbrc:group]"

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
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: read
              type: string
              doc: "Read file in FASTQ, FASTA, or BAM [bvbrc:wsid]"
    doc: "Single end read libraries [bvbrc:group]"

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
              doc: "Sample ID used for filenames"
              default: "sample"
            - name: srr_accession
              type: string
              doc: "SRA Run accession with SRR prefix"
    doc: "SRA read libraries [bvbrc:group]"

  database:
    type: string
    doc: "Target database [enum: bvbrc, Greengenes, SILVA, standard] [bvbrc:enum]"
    default: "SILVA"

  save_classified_sequences:
    type: boolean?
    doc: "Save the classified sequences"
    default: false

  save_unclassified_sequences:
    type: boolean?
    doc: "Save the unclassified sequences"
    default: false

  confidence_interval:
    type: float?
    doc: "Confidence interval for classification (0.0 to 1.0)"
    default: 0.1

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  classification_report:
    type: File?
    doc: "Kraken2 classification report"
    outputBinding:
      glob: "$(inputs.output_file)_report.txt"

  krona_html:
    type: File?
    doc: "Krona interactive visualization (HTML)"
    outputBinding:
      glob: "$(inputs.output_file)_krona.html"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
