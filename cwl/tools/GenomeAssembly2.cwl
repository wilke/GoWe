cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble WGS reads — Assemble reads into a set of contigs"

hints:
  goweHint:
    bvbrc_app_id: GenomeAssembly2
    executor: bvbrc

baseCommand: [GenomeAssembly2]

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
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
  genome_size:
    type: string?
    doc: "Estimated genome size (for canu)"
    default: "5M"
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
