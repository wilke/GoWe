cwlVersion: v1.2
class: CommandLineTool

doc: "Fastq Utilites — Useful common processing of fastq files"

hints:
  goweHint:
    bvbrc_app_id: FastqUtils
    executor: bvbrc

baseCommand: [FastqUtils]

inputs:
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_libs:
    type: string?
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  recipe:
    type: string
    doc: "Recipe"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
