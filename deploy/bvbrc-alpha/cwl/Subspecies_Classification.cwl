cwlVersion: v1.2
class: CommandLineTool

label: subspecies-classification

doc: >-
  Subspecies Classification — Classify viral sequences into subspecies
  or clades using BV-BRC reference alignments. Supports 24 virus types
  including Influenza, SARS-CoV-2, RSV, Dengue, Zika, and others.
  IMPORTANT: All inputs must be NUCLEOTIDE sequences (genomic RNA/DNA).
  This service does NOT accept protein/amino acid sequences.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: SubspeciesClassification
    executor: bvbrc

baseCommand: [SubspeciesClassification]

inputs:
  input_source:
    type: string
    doc: "Source of input [enum: id_list, fasta_data, fasta_file, genome_group] [bvbrc:enum]"

  input_fasta_data:
    type: string?
    doc: "Input sequences in FASTA format (NUCLEOTIDE only — do NOT submit protein/amino acid sequences)"

  input_fasta_file:
    type: string?
    doc: "Input NUCLEOTIDE sequence as a workspace FASTA file (not protein) [bvbrc:wsid]"

  input_genome_group:
    type: string?
    doc: "Input sequence as a workspace genome group [bvbrc:wsid]"

  ref_msa_fasta:
    type: string?
    doc: "Reference multiple sequence alignment in FASTA format [bvbrc:wsid]"

  virus_type:
    type: string
    doc: >-
      Virus type for classification. All reference alignments are nucleotide-based.
      Valid values: MASTADENOA, MASTADENOB, MASTADENOC, MASTADENOE, MASTADENOF,
      NOROORF1, NOROORF2, BOVDIARRHEA1, DENGUE, HCV, JAPENCEPH, MURRAY, STLOUIS,
      TKBENCEPH, WESTNILE, YELLOWFEVER, ZIKA, INFLUENZAH5, INFLUENZAH3N2,
      SWINEH1, SWINEH1US, SWINEH3, MEASLES, MUMPS, MPOX, ROTAA
      [bvbrc:enum]

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  classification_results:
    type: File?
    doc: "Subspecies classification results (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_classification.tsv"

  alignment:
    type: File?
    doc: "Aligned sequences with classification"
    outputBinding:
      glob: "$(inputs.output_file)_alignment.fasta"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
