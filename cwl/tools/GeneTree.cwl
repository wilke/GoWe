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
    doc: "Sequence alphabet: DNA or RNA or Protein [enum: DNA, Protein] [bvbrc:enum]"
  substitution_model:
    type: string?
    doc: "Substitution Model [enum: HKY85, JC69, K80, F81, F84, TN93, GTR, LG, WAG, JTT, MtREV, Dayhoff, DCMut, RtREV, CpREV, VT, AB, Blosum62, MtMam, MtArt, HIVw, HIVb] [bvbrc:enum]"
  bootstrap:
    type: int?
    doc: "Perform boostrapping [bvbrc:integer]"
  recipe:
    type: string?
    doc: "Recipe used for FeatureTree analysis [enum: RAxML, PhyML, FastTree] [bvbrc:enum]"
    default: "RAxML"
  tree_type:
    type: string?
    doc: "Fields to be retrieved for each gene. [enum: viral_genome, gene] [bvbrc:enum]"
  feature_metadata_fields:
    type: string?
    doc: "Fields to be retrieved for each gene."
  genome_metadata_fields:
    type: string?
    doc: "Fields to be retrieved for each genome."
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
