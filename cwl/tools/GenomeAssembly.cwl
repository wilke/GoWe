cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble reads — Assemble reads into a set of contigs"

hints:
  goweHint:
    bvbrc_app_id: GenomeAssembly
    executor: bvbrc

baseCommand: [GenomeAssembly]

inputs:
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  reference_assembly:
    type: string?
    doc: "Reference set of assembled DNA contigs"
  recipe:
    type: string?
    doc: "Recipe used for assembly"
    default: "auto"
  pipeline:
    type: string?
    doc: "Advanced assembly pipeline arguments that overrides recipe"
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
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
