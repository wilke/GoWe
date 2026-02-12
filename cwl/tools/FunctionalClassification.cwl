cwlVersion: v1.2
class: CommandLineTool

doc: "classify reads — Compute functional classification for read data"

hints:
  goweHint:
    bvbrc_app_id: FunctionalClassification
    executor: bvbrc

baseCommand: [FunctionalClassification]

inputs:
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
