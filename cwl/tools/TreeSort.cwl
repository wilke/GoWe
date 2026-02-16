cwlVersion: v1.2
class: CommandLineTool

doc: "TreeSort — Infer reassortment events along branches of a phylogenetic tree"

hints:
  goweHint:
    bvbrc_app_id: TreeSort
    executor: bvbrc

baseCommand: [TreeSort]

inputs:
  input_source:
    type: string
    doc: "Input source [enum: fasta_data, fasta_existing_dataset, fasta_file_id, fasta_group_id] [bvbrc:enum]"
    default: "fasta_file_id"
  input_fasta_data:
    type: string?
    doc: "Input FASTA sequence"
  input_fasta_existing_dataset:
    type: string?
    doc: "Existing dataset directory"
  input_fasta_file_id:
    type: string?
    doc: "Workspace FASTA file ID [bvbrc:wsid]"
  input_fasta_group_id:
    type: string?
    doc: "Workspace genome group ID [bvbrc:wsid]"
  clades_path:
    type: string?
    doc: "Output file path for clades with reassortment"
  deviation:
    type: float?
    doc: "Max deviation from estimated substitution rate"
    default: 2.0
  equal_rates:
    type: boolean?
    doc: "Assume equal rates (no estimation) [bvbrc:bool]"
  inference_method:
    type: string?
    doc: "Inference method [enum: local, mincut] [bvbrc:enum]"
    default: "local"
  match_regex:
    type: string?
    doc: "Custom regex to match segments"
  match_type:
    type: string?
    doc: "Match type [enum: default, epi, regex, strain] [bvbrc:enum]"
    default: "default"
  no_collapse:
    type: boolean?
    doc: "Do not collapse near-zero branches [bvbrc:bool]"
  p_value:
    type: float?
    doc: "P-value cutoff for reassortment tests"
    default: 0.001
  ref_segment:
    type: string?
    doc: "Reference segment"
    default: "HA"
  ref_tree_inference:
    type: string?
    doc: "Reference tree inference method [enum: FastTree, IQTree] [bvbrc:enum]"
    default: "IQTree"
  segments:
    type: string?
    doc: "Segments to analyze (empty=all)"
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
