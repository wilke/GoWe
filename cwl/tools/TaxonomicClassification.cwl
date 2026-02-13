cwlVersion: v1.2
class: CommandLineTool

doc: "classify reads — Compute taxonomic classification for read data"

hints:
  goweHint:
    bvbrc_app_id: TaxonomicClassification
    executor: bvbrc

baseCommand: [TaxonomicClassification]

inputs:
  input_type:
    type: string
    doc: "Input type (reads / contigs) [enum: reads, contigs] [bvbrc:enum]"
  contigs:
    type: File?
    doc: "Input set of DNA contigs for classification [bvbrc:wstype]"
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
  save_classified_sequences:
    type: boolean?
    doc: "Save the classified sequences [bvbrc:bool]"
    default: false
  save_unclassified_sequences:
    type: boolean?
    doc: "Save the unclassified sequences [bvbrc:bool]"
    default: false
  algorithm:
    type: string
    doc: "Classification algorithm [enum: Kraken2] [bvbrc:enum]"
    default: "Kraken2"
  database:
    type: string
    doc: "Target database [enum: Default NT, Kraken2, Greengenes, RDP, SILVA] [bvbrc:enum]"
    default: "Kraken2"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
