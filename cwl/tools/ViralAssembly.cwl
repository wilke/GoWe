cwlVersion: v1.2
class: CommandLineTool

doc: "Viral Assembly — Assemble viral genomes"

hints:
  goweHint:
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
            type: File
            doc: "Forward reads"
          - name: read2
            type: File?
            doc: "Reverse reads"
    doc: "Paired-end reads (singular)"
  single_end_lib:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: File
            doc: "Read file"
    doc: "Single-end reads (singular)"
  srr_id:
    type: string?
    doc: "SRA Run ID"
  strategy:
    type: string?
    doc: "Assembly strategy [enum: auto, irma] [bvbrc:enum]"
    default: "auto"
  module:
    type: string?
    doc: "Virus module [enum: FLU, CoV, RSV, EBOLA, FLU_AD, FLU-utr, FLU-minion] [bvbrc:enum]"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
