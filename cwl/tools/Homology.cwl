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
    doc: "Source of input (id_list, fasta_data, fasta_file) [enum: id_list, fasta_data, fasta_file] [bvbrc:enum]"
  input_fasta_data:
    type: string?
    doc: "Input sequence in fasta formats"
  input_id_list:
    type: string[]?
    doc: "Input sequence as a list of sequence identifiers [bvbrc:array]"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of fasta data [bvbrc:wsid]"
  db_type:
    type: string
    doc: "Database type to search (protein / DNA / RNA / contigs) [enum: faa, ffn, frn, fna] [bvbrc:enum]"
  db_source:
    type: string
    doc: "Source of database (fasta_data, fasta_file, genome_list, taxon_list, precomputed_database) [enum: fasta_data, fasta_file, genome_list, taxon_list, precomputed_database] [bvbrc:enum]"
  db_fasta_data:
    type: string?
    doc: "Database sequences as fasta"
  db_fasta_file:
    type: string?
    doc: "Database fasta file [bvbrc:wsid]"
  db_genome_list:
    type: string[]?
    doc: "Database genome list [bvbrc:array]"
  db_taxon_list:
    type: string[]?
    doc: "Database taxon list [bvbrc:array]"
  db_precomputed_database:
    type: string?
    doc: "Precomputed database name"
  blast_program:
    type: string?
    doc: "BLAST program to use [enum: blastp, blastn, blastx, tblastn, tblastx] [bvbrc:enum]"
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
