cwlVersion: v1.2
class: CommandLineTool

doc: "Compute phylogenetic tree from PGFam protein and DNA sequence — Computes a phylogenetic tree based on protein and DNA sequences of PGFams for a set of genomes"

hints:
  goweHint:
    bvbrc_app_id: CodonTree
    executor: bvbrc

baseCommand: [CodonTree]

inputs:
  output_path:
    type: string
    doc: "Path to which the output will be written "
  output_file:
    type: string
    doc: "Basename for the generated output files"
  genome_ids:
    type: string
    doc: "Main genomes"
  optional_genome_ids:
    type: string?
    doc: "Optional genomes (not penalized for missing/duplicated genes)"
  number_of_genes:
    type: int?
    doc: "Desired number of genes"
    default: "20"
  bootstraps:
    type: int?
    doc: "Number of bootstrap replicates"
    default: "100"
  max_genomes_missing:
    type: int?
    doc: "Number of main genomes allowed missing from any PGFam"
    default: "0"
  max_allowed_dups:
    type: int?
    doc: "Number of duplications allowed for main genomes in any PGFam"
    default: "0"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
