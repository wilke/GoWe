cwlVersion: v1.2
class: CommandLineTool

label: core-genome-mlst

doc: >-
  Core Genome MLST — Evaluate core genomes from a set of genome groups
  of the same species using chewBBACA.

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
    doc: "Genome group workspace path [bvbrc:wsid]"

  input_genome_fasta:
    type: string?
    doc: "Nucleotide data in FASTA format [bvbrc:wsid]"

  schema_location:
    type: string?
    doc: "Path to the parent directory where all schemas are held"

  input_schema_selection:
    type: string
    doc: "Species schema to compare against (correlates to a subdirectory at schema_location)"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  allele_matrix:
    type: File?
    doc: "Allele call matrix (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_allele_matrix.tsv"

  tree_nwk:
    type: File?
    doc: "Phylogenetic tree from allelic profiles (Newick)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"

  statistics:
    type: File?
    doc: "MLST statistics summary"
    outputBinding:
      glob: "$(inputs.output_file)_statistics.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
