cwlVersion: v1.2
class: CommandLineTool

doc: "Core Genome MLST — Evaluate core genomes from a set of genome groups of the same species"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: CoreGenomeMLST
    executor: bvbrc

baseCommand: [CoreGenomeMLST]

inputs:
  input_genome_type:
    type: string
    doc: "Input genome type [enum: genome_group, genome_fasta] [bvbrc:enum]"
  analysis_type:
    type: string
    doc: "Analysis type [enum: chewbbaca] [bvbrc:enum]"
    default: "chewbbaca"
  input_genome_group:
    type: string?
    doc: "Genome group name"
  input_genome_fasta:
    type: File?
    doc: "FASTA data [bvbrc:wstype]"
  schema_location:
    type: string?
    doc: "Schema parent directory path"
  input_schema_selection:
    type: string
    doc: "Species schema to compare against"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  report:
    type: File
    doc: "Interactive HTML report with tree and allele call summary"
    outputBinding:
      glob: "cgMLST_Report.html"
  allele_results:
    type: File
    doc: "Allele call results for all genomes"
    outputBinding:
      glob: "result_alleles.tsv"
  mstree_nwk:
    type: File
    doc: "MSTreeV2 minimum spanning tree (Newick)"
    outputBinding:
      glob: "*_MSTreeV2.tre"
  mstree_phyloxml:
    type: File?
    doc: "MSTreeV2 tree (PhyloXML with genome metadata)"
    outputBinding:
      glob: "*_MSTreeV2.tre.phyloxml"
  mstree_svg:
    type: File?
    doc: "MSTreeV2 tree visualization (SVG)"
    outputBinding:
      glob: "*_MSTreeV2.tre.svg"
  distance_matrix:
    type: File?
    doc: "Pairwise allelic distance matrix (PHYLIP)"
    outputBinding:
      glob: "*_distance.phylip"
  cluster_result:
    type: File?
    doc: "Hierarchical clustering result (pHierCC)"
    outputBinding:
      glob: "*_cluster_result_cgMLSTv1.HierCC"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
