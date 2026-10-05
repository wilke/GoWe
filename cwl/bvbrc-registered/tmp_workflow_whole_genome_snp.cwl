cwlVersion: v1.2
class: CommandLineTool

label: whole-genome-snp-analysis

doc: >-
  Whole Genome SNP Analysis — Identify SNP differences in a genome group
  with genomes of the same species.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: WholeGenomeSNPAnalysis
    executor: bvbrc

baseCommand: [WholeGenomeSNPAnalysis]

inputs:
  input_genome_type:
    type: string
    doc: "Input genome type [enum: genome_group, genome_fasta] [bvbrc:enum]"

  analysis_type:
    type: string
    doc: "Analysis type [enum: Whole Genome SNP Analysis] [bvbrc:enum]"

  input_genome_group:
    type: string?
    doc: "Genome group workspace path [bvbrc:wsid]"

  input_genome_fasta:
    type: string?
    doc: "Nucleotide data in FASTA format [bvbrc:wsid]"

  majority-threshold:
    type: float?
    doc: "Minimum fraction of genomes with locus for tree calculation"
    default: 0.5

  min_mid_linkage:
    type: int?
    doc: "Minimum value for mid linkage (also maximum for strong linkage)"
    default: 10

  max_mid_linkage:
    type: int?
    doc: "Maximum value for mid linkage (also minimum for weak linkage)"
    default: 40

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  snp_table:
    type: File?
    doc: "SNP distance matrix"
    outputBinding:
      glob: "$(inputs.output_file)_snp_distance.tsv"

  tree_nwk:
    type: File?
    doc: "Phylogenetic tree from SNP analysis (Newick)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"

  vcf:
    type: File?
    doc: "Merged variant calls (VCF)"
    outputBinding:
      glob: "$(inputs.output_file).vcf"

  alignment:
    type: File?
    doc: "Core SNP alignment (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
