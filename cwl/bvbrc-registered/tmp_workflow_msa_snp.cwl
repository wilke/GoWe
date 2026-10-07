cwlVersion: v1.2
class: CommandLineTool

doc: "Multiple sequence alignment variation service — Compute the multiple sequence alignment and analyze SNP/variance."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: MSA
    executor: bvbrc

baseCommand: [MSA]

inputs:
  fasta_files:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: fasta_file
          fields:
            - name: file
              type: File
              doc: "FASTA sequence file"
            - name: type
              type: string?
              doc: "File type [enum: feature_dna_fasta, feature_protein_fasta] [bvbrc:enum]"
    doc: " [bvbrc:group]"
  feature_groups:
    type: string[]?
    doc: "Workspace paths of Feature Groups to align (one or more). Use this when the user names a Feature Group. [bvbrc:wsid]"
  select_genomegroup:
    type: string[]?
    doc: "Workspace paths of Genome Groups to align (one or more) [bvbrc:wsid]"
  aligner:
    type: string?
    doc: "Tool used for aligning multiple sequences to each other. [enum: Muscle, Mafft] [bvbrc:enum]"
    default: "Muscle"
  alphabet:
    type: string
    doc: "Determines which sequence alphabet is present. [enum: dna, protein] [bvbrc:enum]"
    default: "dna"
  fasta_keyboard_input:
    type: string?
    doc: "Text input for a fasta file."
  ref_type:
    type: string?
    default: "none"
    doc: "Reference sequence selection [enum: none, first, string, feature_id, genome_id] [bvbrc:enum]. Keep none unless the user asks for a reference."
  ref_string:
    type: string?
    default: ""
    doc: "Reference sequence text, feature ID, or genome ID — matches ref_type. Required when ref_type is string, feature_id, or genome_id."
  strategy:
    type: string?
    doc: "MAFFT strategy [enum: auto, linsi, ginsi, einsi, fftns1, fftns2, fftnsi]. Only used when aligner is Mafft."
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"

outputs:
  alignment:
    type: File
    doc: "Multiple sequence alignment (FASTA format)"
    outputBinding:
      glob: "$(inputs.output_file).afa"
  consensus:
    type: File
    doc: "Consensus sequence (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).consensus.fasta"
  snp_table:
    type: File?
    doc: "SNP analysis table (TSV)"
    outputBinding:
      glob: "$(inputs.output_file).snp.tsv"
  gene_tree:
    type: File?
    doc: "Gene tree (Newick format)"
    outputBinding:
      glob: "$(inputs.output_file)_fasttree.nwk"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
