cwlVersion: v1.2
class: CommandLineTool

doc: "Gene Tree — Estimate phylogeny of gene or other sequence feature"

hints:
  goweHint:
    bvbrc_app_id: GeneTree
    executor: bvbrc

baseCommand: [GeneTree]

inputs:
  sequences:
    type: string
    doc: "Sequence Data Inputs"
  alignment_program:
    type: string?
    doc: "Alignment Program"
  trim_threshold:
    type: float?
    doc: "Alignment End-Trimming Threshold"
  gap_threshold:
    type: float?
    doc: "Delete Gappy Sequences Threshold"
  alphabet:
    type: string
    doc: "Sequence alphabet: DNA or RNA or Protein"
  substitution_model:
    type: string?
    doc: "Substitution Model"
  bootstrap:
    type: int?
    doc: "Perform boostrapping"
  recipe:
    type: string?
    doc: "Recipe used for FeatureTree analysis"
    default: "RAxML"
  tree_type:
    type: string?
    doc: "Fields to be retrieved for each gene."
  feature_metadata_fields:
    type: string?
    doc: "Fields to be retrieved for each gene."
  genome_metadata_fields:
    type: string?
    doc: "Fields to be retrieved for each genome."
  output_path:
    type: Directory
    doc: "Path to which the output will be written."
  output_file:
    type: string
    doc: "Basename for the generated output files."

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
