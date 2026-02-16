cwlVersion: v1.2
class: CommandLineTool

doc: "Whole Genome SNP Analysis — Identify SNP differences in a genome group"

hints:
  goweHint:
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
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
