cwlVersion: v1.2
class: CommandLineTool

label: treesort

doc: >-
  TreeSort — Infer both recent and ancestral reassortment events along
  the branches of a phylogenetic tree of a fixed genomic segment.
  Designed for segmented viruses such as influenza.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: TreeSort
    executor: bvbrc

baseCommand: [TreeSort]

inputs:
  input_source:
    type: string
    doc: "Input source [enum: fasta_data, fasta_existing_dataset, fasta_file_id, genome_group] [bvbrc:enum]"
    default: "fasta_file_id"

  input_fasta_data:
    type: string?
    doc: "Input FASTA sequence data"

  input_fasta_existing_dataset:
    type: string?
    doc: "Directory with previously prepared dataset files"
    default: ""

  input_fasta_file_id:
    type: string?
    doc: "Full workspace PATH to the input FASTA file (e.g. /user@patricbrc.org/home/folder/file.fasta) — use the file path, NOT the workspace object UUID [bvbrc:wsid]"

  input_fasta_group_id:
    type: string?
    doc: "Full workspace PATH to the genome group (e.g. /user@patricbrc.org/home/folder/group) — use the path, NOT the workspace object UUID [bvbrc:wsid]"

  ref_segment:
    type: string?
    doc: "Reference segment for the phylogenetic tree"
    default: "HA"

  ref_tree_inference:
    type: string?
    doc: "Reference tree inference method [enum: FastTree, IQTree] [bvbrc:enum]"
    default: "IQTree"

  inference_method:
    type: string?
    doc: "Reassortment inference method [enum: local, mincut] [bvbrc:enum]"
    default: "local"

  segments:
    type: string?
    doc: "Segments to analyze (empty string = all segments)"
    default: ""

  match_type:
    type: string?
    doc: "How to match segments across alignments [enum: default, epi, regex, strain] [bvbrc:enum]"
    default: "default"

  match_regex:
    type: string?
    doc: "Custom regex to match segments (used when match_type is regex)"
    default: ""

  p_value:
    type: float?
    doc: "P-value cutoff for reassortment tests"
    default: 0.001

  deviation:
    type: float?
    doc: "Maximum deviation from estimated substitution rate"
    default: 2.0

  equal_rates:
    type: boolean?
    doc: "Assume equal rates across segments (skip rate estimation) [bvbrc:bool]"
    default: false

  no_collapse:
    type: boolean?
    doc: "Do not collapse near-zero length branches into multifurcations [bvbrc:bool]"
    default: false

  clades_path:
    type: string?
    doc: "Output file path for clades with evidence of reassortment"
    default: ""

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  reassortment_report:
    type: File?
    doc: "Reassortment inference report"
    outputBinding:
      glob: "$(inputs.output_file)_reassortment.tsv"

  tree:
    type: File?
    doc: "Annotated phylogenetic tree (Newick)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
