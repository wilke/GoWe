cwlVersion: v1.2
class: CommandLineTool

hints:
  goweHint:
    bvbrc_app_id: GenomeAssembly2
    executor: bvbrc

baseCommand: ["true"]

inputs:
  read1:
    type: File
    doc: "Forward reads"
  read2:
    type: File
    doc: "Reverse reads"
  recipe:
    type: string
    default: "auto"
    doc: "Assembly algorithm"

outputs:
  contigs:
    type: File
    outputBinding:
      glob: "*.contigs.fasta"
