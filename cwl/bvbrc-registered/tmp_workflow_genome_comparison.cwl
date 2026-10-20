cwlVersion: v1.2
class: CommandLineTool

label: proteome-comparison

doc: >-
  Proteome Comparison — Compare the proteome sets from multiple genomes
  using BLAST-based bidirectional best hits.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: GenomeComparison
    executor: bvbrc

baseCommand: [GenomeComparison]

inputs:
  genome_ids:
    type: string[]?
    doc: "BV-BRC genome IDs to compare [bvbrc:array]"

  user_genomes:
    type: string[]?
    doc: "Genome protein sequence files in FASTA [bvbrc:wsid]"

  user_feature_groups:
    type: string[]?
    doc: "User feature group workspace paths [bvbrc:wsid]"

  reference_genome_index:
    type: int?
    doc: "Position of the reference, 1-based, counted over genome_ids first, then user_genomes, then user_feature_groups. A reference genome id goes first in genome_ids (index 1); a reference FASTA is len(genome_ids) + its position in user_genomes; a reference feature group is len(genome_ids) + len(user_genomes) + its position in user_feature_groups."
    default: 1

  min_seq_cov:
    type: float?
    doc: "Minimum coverage of query and subject"
    default: 0.30

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
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  comparison_table:
    type: File?
    doc: "Proteome comparison results table (JSON)"
    outputBinding:
      glob: "$(inputs.output_file).json"

  circos_svg:
    type: File?
    doc: "Circos visualization (SVG)"
    outputBinding:
      glob: "$(inputs.output_file).svg"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
