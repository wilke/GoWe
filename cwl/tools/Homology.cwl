cwlVersion: v1.2
class: CommandLineTool

label: homology

doc: "Perform homology searches — runs BLAST against a precomputed database, fasta data, fasta file, genome list, or taxon list. Wraps BV-BRC's Homology app."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: Homology
    executor: bvbrc

baseCommand: [Homology]

inputs:
  input_type:
    type: string
    doc: "Type of input (dna or aa) [enum: dna, aa] [bvbrc:enum]"
  input_source:
    type: string
    doc: "Source of input [enum: id_list, fasta_data, fasta_file] [bvbrc:enum]"
  input_fasta_data:
    type: string?
    doc: "Input sequence in FASTA format"
  input_id_list:
    type: string[]?
    doc: "Input sequence as a list of sequence identifiers [bvbrc:array]"
  input_fasta_file:
    type: string?
    doc: "Input sequence as a workspace file of FASTA data [bvbrc:wsid]"
  db_type:
    type: string
    doc: "Database type to search [enum: faa, ffn, frn, fna] [bvbrc:enum]"
  db_source:
    type: string
    doc: "Source of database [enum: fasta_data, fasta_file, genome_list, taxon_list, precomputed_database] [bvbrc:enum]"
  db_fasta_data:
    type: string?
    doc: "Database sequences as FASTA"
  db_fasta_file:
    type: string?
    doc: "Database FASTA file [bvbrc:wsid]"
  db_genome_list:
    type: string[]?
    doc: "Database genome list [bvbrc:array]"
  db_taxon_list:
    type: string[]?
    doc: "Database taxon list [bvbrc:array]"
  db_precomputed_database:
    type: string?
    doc: "Precomputed database name (e.g. bacteria-archaea)"
  blast_program:
    type: string?
    doc: "BLAST program [enum: blastp, blastn, blastx, tblastn, tblastx] [bvbrc:enum]"
  blast_evalue_cutoff:
    type: float?
    doc: "E-value cutoff for BLAST hits (-evalue)"
    default: 1.0e-5
  blast_max_hits:
    type: int?
    doc: "Maximum number of BLAST hits to return (-max_target_seqs)"
    default: 300
  blast_min_coverage:
    type: int?
    doc: "Minimum HSP query coverage percent (-qcov_hsp_perc)"
  output_path:
    type: Directory
    doc: "Path to which the output will be written [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  blast_json:
    type: File
    doc: "Processed BLAST results in JSON format (gnl| stripped, query IDs cleaned)"
    outputBinding:
      glob: "blast_out.json"
  blast_raw_json:
    type: File?
    doc: "Raw BLAST JSON output (blast_formatter -outfmt 15)"
    outputBinding:
      glob: "blast_out.raw.json"
  blast_table:
    type: File?
    doc: "Tabular hits (-outfmt 6): qseqid sseqid pident length mismatch gapopen qstart qend sstart send evalue bitscore qlen slen"
    outputBinding:
      glob: "blast_out.txt"
  blast_headers:
    type: File?
    doc: "TSV header line describing blast_out.txt columns"
    outputBinding:
      glob: "blast_headers.txt"
  blast_metadata:
    type: File?
    doc: "Hit-ID to decoded title metadata map (JSON)"
    outputBinding:
      glob: "blast_out.metadata.json"
  blast_archive:
    type: File?
    doc: "BLAST ASN.1 archive (-outfmt 11)"
    outputBinding:
      glob: "blast_out.archive"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
