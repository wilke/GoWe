cwlVersion: v1.2
class: CommandLineTool

doc: "Compute phylogenetic tree from PGFam protein and DNA sequence — Computes a phylogenetic tree based on protein and DNA sequences of PGFams for a set of genomes"

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
    doc: "Path to which the output will be written  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"
  genome_ids:
    type: string[]
    doc: "Main genomes [bvbrc:list]"
  optional_genome_ids:
    type: string[]?
    doc: "Optional genomes (not penalized for missing/duplicated genes) [bvbrc:list]"
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
  newick_tree:
    type: File
    doc: "Phylogenetic tree (Newick format)"
    outputBinding:
      glob: "$(inputs.output_file).nwk"
  phyloxml_tree:
    type: File
    doc: "Phylogenetic tree (PhyloXML format)"
    outputBinding:
      glob: "$(inputs.output_file).phyloxml"
  tree_svg:
    type: File?
    doc: "Tree visualization (SVG)"
    outputBinding:
      glob: "$(inputs.output_file).svg"
  tree_png:
    type: File?
    doc: "Tree visualization (PNG)"
    outputBinding:
      glob: "$(inputs.output_file).png"
  alignment:
    type: File?
    doc: "Aligned sequences (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"
  tree_html:
    type: File?
    doc: "Interactive tree visualization (HTML)"
    outputBinding:
      glob: "$(inputs.output_file).html"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
