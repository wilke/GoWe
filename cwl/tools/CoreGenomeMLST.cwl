cwlVersion: v1.2
class: CommandLineTool

doc: "Core Genome MLST — Evaluate core genomes from a set of genome groups of the same species"

hints:
  goweHint:
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
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
