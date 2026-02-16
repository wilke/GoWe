cwlVersion: v1.2
class: CommandLineTool

doc: "Perform homology searches — Perform homology searches on sequence data"

hints:
  goweHint:
    bvbrc_app_id: Homology
    executor: bvbrc

baseCommand: [Homology]

inputs:
  input_type:
    type: string
    doc: "Type of input (dna or aa) [enum: dna, aa] [bvbrc:enum]"
  input_source:
    type: string
    doc: "Source of input [enum: id_list, fasta_data, fasta_file, feature_group, genome_group_features, genome_group_sequences] [bvbrc:enum]"
  input_fasta_data:
    type: string?
    doc: "Input sequence in fasta format"
  input_id_list:
    type: string[]?
    doc: "Input sequence as a list of sequence identifiers [bvbrc:array]"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of fasta data [bvbrc:wsid]"
  input_feature_group:
    type: string?
    doc: "Feature group [bvbrc:wsid]"
  input_genome_group:
    type: string?
    doc: "Genome group [bvbrc:wsid]"
  db_type:
    type: string
    doc: "Database type to search (protein / DNA / RNA / contigs) [enum: faa, ffn, frn, fna] [bvbrc:enum]"
  db_source:
    type: string
    doc: "Source of database [enum: id_list, fasta_data, fasta_file, genome_list, feature_group, genome_group, taxon_list, precomputed_database] [bvbrc:enum]"
  db_fasta_data:
    type: string?
    doc: "Database sequences as fasta"
  db_fasta_file:
    type: string?
    doc: "Database fasta file [bvbrc:wsid]"
  db_id_list:
    type: string[]?
    doc: "Database sequence IDs [bvbrc:array]"
  db_genome_list:
    type: string[]?
    doc: "Database genome list [bvbrc:array]"
  db_feature_group:
    type: string?
    doc: "Database feature group [bvbrc:wsid]"
  db_genome_group:
    type: string?
    doc: "Database genome group [bvbrc:wsid]"
  db_taxon_list:
    type: string[]?
    doc: "Database taxon list [bvbrc:array]"
  db_precomputed_database:
    type: string?
    doc: "Precomputed database name"
  blast_program:
    type: string?
    doc: "BLAST program to use [enum: blastp, blastn, blastx, tblastn, tblastx] [bvbrc:enum]"
  blast_evalue_cutoff:
    type: float?
    doc: "E-value cutoff"
    default: 1e-5
  blast_max_hits:
    type: int?
    doc: "Max hits"
    default: 300
  blast_min_coverage:
    type: int?
    doc: "Min coverage"
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
