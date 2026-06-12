cwlVersion: v1.2
class: CommandLineTool

doc: "Whole Genome SNP Analysis — Identify SNP differences in a genome group"

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
  majority-threshold:
    type: float?
    doc: "Min fraction of genomes with locus"
    default: 0.5
  min_mid_linkage:
    type: int?
    doc: "Min mid linkage (max strong linkage)"
    default: 10
  max_mid_linkage:
    type: int?
    doc: "Max mid linkage (min weak linkage)"
    default: 40
  analysis_type:
    type: string
    doc: "Analysis type [enum: Whole Genome SNP Analysis] [bvbrc:enum]"
  input_genome_group:
    type: string?
    doc: "Genome group name"
  input_genome_fasta:
    type: File?
    doc: "FASTA data [bvbrc:wstype]"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  report:
    type: File
    doc: "Interactive HTML report with heatmaps and tree visualizations"
    outputBinding:
      glob: "WholeGenomeSNP_Report.html"
  core_snps:
    type: File
    doc: "Core SNP calls (present in all genomes)"
    outputBinding:
      glob: "core_SNPs.tsv"
  all_snps:
    type: File
    doc: "All SNP calls"
    outputBinding:
      glob: "SNPs_all.tsv"
  core_dist_matrix:
    type: File?
    doc: "Pairwise SNP distance matrix (core SNPs)"
    outputBinding:
      glob: "core_kSNPdist.matrix"
  all_dist_matrix:
    type: File?
    doc: "Pairwise SNP distance matrix (all SNPs)"
    outputBinding:
      glob: "all_kSNPdist.matrix"
  core_snps_matrix:
    type: File?
    doc: "Core SNP alignment matrix"
    outputBinding:
      glob: "core_SNPs_matrix.txt"
  all_snps_matrix:
    type: File?
    doc: "All SNPs alignment matrix"
    outputBinding:
      glob: "SNPs_all_matrix.txt"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
