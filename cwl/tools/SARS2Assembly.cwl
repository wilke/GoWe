cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble SARS2 reads — Assemble SARS2 reads into a consensus sequence"

hints:
  goweHint:
    bvbrc_app_id: SARS2Assembly
    executor: bvbrc

baseCommand: [SARS2Assembly]

inputs:
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
  max_depth:
    type: int?
    doc: "Maximum read depth to consider for consensus sequence"
    default: 8000
  keep_intermediates:
    type: int?
    doc: "Keep all intermediate output from the pipeline"
    default: 0
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  debug_level:
    type: int?
    doc: "Debugging level."
    default: 0

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
