cwlVersion: v1.2
class: CommandLineTool

doc: "Blast-based genome proteome comparison — Compare the proteome sets from multiple genomes using Blast"

hints:
  goweHint:
    bvbrc_app_id: GenomeComparison
    executor: bvbrc

baseCommand: [GenomeComparison]

inputs:
  genome_ids:
    type: string?
    doc: "Genome IDs"
  user_genomes:
    type: string?
    doc: "Genome protein sequence files in FASTA"
  user_feature_groups:
    type: string?
    doc: "User feature groups"
  reference_genome_index:
    type: int?
    doc: "Index of genome to be used as reference (1-based)"
    default: 1
  min_seq_cov:
    type: float?
    doc: "Minimum coverage of query and subject"
    default: 0.3
  max_e_val:
    type: float?
    doc: "Maximum E-value"
    default: 1e-05
  min_ident:
    type: float?
    doc: "Minimum fraction identity"
    default: 0.1
  min_positives:
    type: float?
    doc: "Minimum fraction positive-scoring positions"
    default: 0.2
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
