cwlVersion: v1.2
class: Workflow

label: blast-protein-search

doc: |
  BLAST homology search wrapper — runs BV-BRC's Homology app via a
  gowe://Homology reference so updates to Homology.cwl propagate
  automatically. Replaces the original blast-protein-search workflow
  which inlined a stale Homology definition with broken shell-style
  output globs.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

inputs:
  blast_search_input_type:
    type: string
    default: "aa"
    doc: "dna or aa"
  blast_search_input_source:
    type: string
    default: "fasta_data"
    doc: "id_list, fasta_data, or fasta_file"
  blast_search_input_fasta_data:
    type: string
    doc: "Input sequence in FASTA format"
  blast_search_db_type:
    type: string
    default: "faa"
    doc: "Database type: faa, ffn, frn, fna"
  blast_search_db_source:
    type: string
    default: "precomputed_database"
    doc: "fasta_data, fasta_file, genome_list, taxon_list, precomputed_database"
  blast_search_db_precomputed_database:
    type: string
    default: "bacteria-archaea"
    doc: "Name of a BV-BRC precomputed database"
  blast_search_blast_program:
    type: string
    default: "blastp"
    doc: "blastp, blastn, blastx, tblastn, or tblastx"
  blast_search_blast_evalue_cutoff:
    type: float?
    default: 1.0e-5
  blast_search_blast_max_hits:
    type: int?
    default: 300
  blast_search_blast_min_coverage:
    type: int?
  output_path:
    type: string
    doc: "Workspace folder path (e.g. /user@bvbrc/home/blast-test) [bvbrc:folder]"
  output_file:
    type: string
    default: "blast_results"
    doc: "Basename for output files [bvbrc:wsid]"

steps:
  blast_search:
    run: "gowe://Homology"
    in:
      input_type: blast_search_input_type
      input_source: blast_search_input_source
      input_fasta_data: blast_search_input_fasta_data
      db_type: blast_search_db_type
      db_source: blast_search_db_source
      db_precomputed_database: blast_search_db_precomputed_database
      blast_program: blast_search_blast_program
      blast_evalue_cutoff: blast_search_blast_evalue_cutoff
      blast_max_hits: blast_search_blast_max_hits
      blast_min_coverage: blast_search_blast_min_coverage
      output_path: output_path
      output_file: output_file
    out: [blast_json, blast_raw_json, blast_table, blast_headers, blast_metadata, blast_archive, result_folder]

outputs:
  blast_json:
    type: File
    doc: "Processed BLAST results (JSON)"
    outputSource: blast_search/blast_json
  blast_raw_json:
    type: File?
    doc: "Raw BLAST JSON (-outfmt 15)"
    outputSource: blast_search/blast_raw_json
  blast_table:
    type: File?
    doc: "Tabular hits (-outfmt 6)"
    outputSource: blast_search/blast_table
  blast_headers:
    type: File?
    doc: "Column header for the tabular hits"
    outputSource: blast_search/blast_headers
  blast_metadata:
    type: File?
    doc: "Hit-ID to title metadata (JSON)"
    outputSource: blast_search/blast_metadata
  blast_archive:
    type: File?
    doc: "BLAST ASN.1 archive (-outfmt 11)"
    outputSource: blast_search/blast_archive
  result_folder:
    type: Directory
    doc: "Full output folder in workspace"
    outputSource: blast_search/result_folder
