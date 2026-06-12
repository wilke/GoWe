cwlVersion: v1.2
class: CommandLineTool

label: codon-tree

doc: "Compute phylogenetic tree from PGFam protein and DNA sequence — RAxML-based codon tree from single-copy PGFams across a set of genomes. Replaces the deprecated PhylogeneticTree app."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: CodonTree
    executor: bvbrc

baseCommand: [CodonTree]

inputs:
  output_path:
    type: Directory
    doc: "Path to which the output will be written [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"
  genome_ids:
    type: string[]
    doc: "Main genomes (must have PGFam coverage) [bvbrc:list]"
  optional_genome_ids:
    type: string[]?
    doc: "Optional genomes (not penalized for missing/duplicated genes) [bvbrc:list]"
  number_of_genes:
    type: int?
    doc: "Desired number of single-copy PGFams to build the tree from"
    default: 20
  bootstraps:
    type: int?
    doc: "Number of bootstrap replicates"
    default: 100
  max_genomes_missing:
    type: int?
    doc: "Number of main genomes allowed missing from any PGFam"
    default: 0
  max_allowed_dups:
    type: int?
    doc: "Number of duplications allowed for main genomes in any PGFam"
    default: 0

outputs:
  tree_nwk:
    type: File
    doc: "Best phylogenetic tree (Newick, RAxML)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"
  tree_phyloxml:
    type: File?
    doc: "Phylogenetic tree (PhyloXML with metadata)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.phyloxml"
  report:
    type: File
    doc: "HTML report with tree visualization and statistics"
    outputBinding:
      glob: "$(inputs.output_file)_report.html"
  tree_svg:
    type: File?
    doc: "Tree visualization (SVG)"
    outputBinding:
      glob: "$(inputs.output_file).svg"
  alignment:
    type: File?
    doc: "Concatenated protein alignment (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).afa"
  detail_files:
    type: Directory?
    doc: "Auxiliary outputs (rooted tree, RAxML artifacts, stats, logs)"
    outputBinding:
      glob: "detail_files"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
