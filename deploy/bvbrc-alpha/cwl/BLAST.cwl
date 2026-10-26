cwlVersion: v1.2
class: CommandLineTool

label: blast-homology

doc: >-
  Perform homology searches — runs BLAST against a precomputed database,
  fasta data, fasta file, genome list, or taxon list.
  Wraps BV-BRC's Homology app.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: Homology
    executor: bvbrc

baseCommand: [Homology]

inputs:
  input_source:
    type: string
    doc: >-
      Source of the query sequences [enum: feature_group, fasta_file, fasta_data, id_list]
      [bvbrc:enum]. Use feature_group when the user has a BV-BRC Feature Group (then set
      input_feature_group). Use fasta_file for a workspace FASTA (input_fasta_file), fasta_data
      for pasted sequence text (input_fasta_data). id_list (input_id_list) is a list of BV-BRC
      feature IDs — only use it when the user gives explicit feature IDs.

  input_type:
    type: string
    doc: "Type of input (dna or aa) [enum: dna, aa] [bvbrc:enum]"

  input_feature_group:
    type: string?
    doc: "Workspace path of the query Feature Group. Required when input_source=feature_group [bvbrc:wsid]"

  input_fasta_data:
    type: string?
    doc: "Query sequences as FASTA text. Required when input_source=fasta_data"

  input_fasta_file:
    type: string?
    doc: "Workspace path of the query FASTA file. Required when input_source=fasta_file [bvbrc:wsid]"

  input_id_list:
    type: string[]?
    doc: "BV-BRC feature IDs. Required when input_source=id_list [bvbrc:array]"

  blast_program:
    type: string?
    doc: "BLAST program [enum: blastp, blastn, blastx, tblastn, tblastx] [bvbrc:enum]"

  blast_evalue_cutoff:
    type: float?
    default: 1e-05
    doc: "E-value cutoff for BLAST hits (-evalue)"

  blast_max_hits:
    type: int?
    default: 10
    doc: "Maximum number of BLAST hits to return (-max_target_seqs). Keep the default unless the user asks for more."

  blast_min_coverage:
    type: int?
    doc: "Minimum HSP query coverage percent (-qcov_hsp_perc)"

  db_source:
    type: string
    doc: >-
      Database to search [enum: precomputed_database, genome_list, genome_group, feature_group,
      taxon_list, fasta_file, fasta_data] [bvbrc:enum]. Each value requires its payload input:
      precomputed_database→db_precomputed_database, genome_list→db_genome_list,
      genome_group→db_genome_group, feature_group→db_feature_group, taxon_list→db_taxon_list,
      fasta_file→db_fasta_file, fasta_data→db_fasta_data.

  db_type:
    type: string
    doc: "Database type to search [enum: faa, ffn, frn, fna] [bvbrc:enum]"

  db_precomputed_database:
    type: string?
    doc: "Precomputed database name (e.g. bacteria-archaea). Required when db_source=precomputed_database"

  db_fasta_data:
    type: string?
    doc: "Database sequences as FASTA text. Required when db_source=fasta_data"

  db_fasta_file:
    type: string?
    doc: "Database FASTA file. Required when db_source=fasta_file [bvbrc:wsid]"

  db_genome_list:
    type: string[]?
    doc: "BV-BRC genome IDs to search. Required when db_source=genome_list [bvbrc:array]"

  db_genome_group:
    type: string?
    doc: "Workspace path of a Genome Group to search. Required when db_source=genome_group [bvbrc:wsid]"

  db_feature_group:
    type: string?
    doc: "Workspace path of a Feature Group to search. Required when db_source=feature_group [bvbrc:wsid]"

  db_taxon_list:
    type: string[]?
    doc: "Taxon IDs to search. Required when db_source=taxon_list [bvbrc:array]"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  blast_json:
    type: File?
    doc: "Processed BLAST results in JSON format"
    outputBinding:
      glob: "$(inputs.output_file).blast_out.json"

  blast_raw_json:
    type: File?
    doc: "Raw BLAST JSON output (blast_formatter -outfmt 15)"
    outputBinding:
      glob: "$(inputs.output_file).blast_out.raw.json"

  blast_table:
    type: File?
    doc: "Tabular hits (-outfmt 6)"
    outputBinding:
      glob: "$(inputs.output_file).blast_out.txt"

  blast_metadata:
    type: File?
    doc: "Hit-ID to decoded title metadata map (JSON)"
    outputBinding:
      glob: "$(inputs.output_file).blast_out.metadata.json"

  blast_archive:
    type: File?
    doc: "BLAST ASN.1 archive (-outfmt 11)"
    outputBinding:
      glob: "$(inputs.output_file).blast_out.archive"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
