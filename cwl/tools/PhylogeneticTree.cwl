cwlVersion: v1.2
class: CommandLineTool

doc: "Compute phylogenetic tree — Computes a phylogenetic tree given a set of in-group and out-group genomes"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: PhylogeneticTree
    executor: bvbrc

baseCommand: [PhylogeneticTree]

inputs:
  output_path:
    type: Directory
    doc: "Path to which the output will be written.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"
  in_genome_ids:
    type: string[]?
    doc: "In-group genomes [bvbrc:list]"
  out_genome_ids:
    type: string[]?
    doc: "Out-group genomes [bvbrc:list]"
  genome_groups:
    type: string[]?
    doc: "Genome groups (workspace paths) [bvbrc:list]"
  full_tree_method:
    type: string?
    doc: "Full tree method"
    default: "ml"
  refinement:
    type: string?
    doc: "Automated progressive refinement"
    default: "yes"

outputs:
  tree_nwk:
    type: File
    doc: "Phylogenetic tree (Newick format, RAxML)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"
  tree_phyloxml:
    type: File
    doc: "Phylogenetic tree (PhyloXML with genome metadata)"
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
    doc: "Concatenated protein+codon alignment (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).afa"
  alignment_stats:
    type: File?
    doc: "Per-PGFam alignment statistics"
    outputBinding:
      glob: "$(inputs.output_file).homologAlignmentStats.txt"
  analysis_stats:
    type: File?
    doc: "Summary statistics (genomes, alignments, positions, runtime)"
    outputBinding:
      glob: "$(inputs.output_file).analysisStats"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
