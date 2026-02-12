cwlVersion: v1.2
class: CommandLineTool

doc: "Compute phylogenetic tree — Computes a phylogenetic tree given a set of in-group and out-group genomes"

hints:
  goweHint:
    bvbrc_app_id: PhylogeneticTree
    executor: bvbrc

baseCommand: [PhylogeneticTree]

inputs:
  output_path:
    type: Directory
    doc: "Path to which the output will be written. "
  output_file:
    type: string
    doc: "Basename for the generated output files."
  in_genome_ids:
    type: string
    doc: "In-group genomes"
  out_genome_ids:
    type: string
    doc: "Out-group genomes"
  full_tree_method:
    type: string?
    doc: "Full tree method"
    default: "ml"
  refinement:
    type: string?
    doc: "Automated progressive refinement"
    default: "yes"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
