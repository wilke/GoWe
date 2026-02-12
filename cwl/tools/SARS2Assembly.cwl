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
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  recipe:
    type: string?
    doc: "Recipe used for assembly"
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
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  debug_level:
    type: int?
    doc: "Debugging level."
    default: 0

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
