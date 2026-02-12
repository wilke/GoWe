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
    doc: "Type of input (dna or aa)"
  input_source:
    type: string
    doc: "Source of input (id_list, fasta_data, fasta_file)"
  input_fasta_data:
    type: string?
    doc: "Input sequence in fasta formats"
  input_id_list:
    type: string?
    doc: "Input sequence as a list of sequence identifiers"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of fasta data"
  db_type:
    type: string
    doc: "Database type to search (protein / DNA / RNA / contigs)"
  db_source:
    type: string
    doc: "Source of database (fasta_data, fasta_file, genome_list, taxon_list, precomputed_database)"
  db_fasta_data:
    type: string?
    doc: "Database sequences as fasta"
  db_fasta_file:
    type: string?
    doc: "Database fasta file"
  db_genome_list:
    type: string?
    doc: "Database genome list"
  db_taxon_list:
    type: string?
    doc: "Database taxon list"
  db_precomputed_database:
    type: string?
    doc: "Precomputed database name"
  blast_program:
    type: string?
    doc: "BLAST program to use"
  output_path:
    type: string
    doc: "Path to which the output will be written."
  output_file:
    type: string
    doc: "Basename for the generated output files."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
