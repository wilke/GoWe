cwlVersion: v1.2
class: CommandLineTool

doc: "Analyze TnSeq data — Use TRANSIT to analyze TnSeq data"

hints:
  goweHint:
    bvbrc_app_id: TnSeq
    executor: bvbrc

baseCommand: [TnSeq]

inputs:
  experimental_conditions:
    type: string?
    doc: "Experimental conditions"
  contrasts:
    type: string?
    doc: "Contrasts"
  read_files:
    type: string?
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  recipe:
    type: string?
    doc: "Recipe used for TnSeq analysis"
    default: "gumbel"
  protocol:
    type: string?
    doc: "Protocol used for TnSeq analysis"
    default: "sassetti"
  primer:
    type: string?
    doc: "Primer DNA string for read trimming."
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data."
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
