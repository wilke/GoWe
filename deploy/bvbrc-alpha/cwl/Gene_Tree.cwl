cwlVersion: v1.2
class: CommandLineTool

label: gene-tree

doc: >-
  Gene Tree — Estimate phylogeny of a gene or other sequence feature
  using RAxML, PhyML, or FastTree.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: GeneTree
    executor: bvbrc

baseCommand: [GeneTree]

inputs:
  sequences:
    type:
      type: array
      items:
        type: record
        name: sequence_input
        fields:
          - name: filename
            type: string
            doc: "Workspace path of the input (Feature Group, Genome Group, or FASTA file) [bvbrc:wsid]"
          - name: type
            type: string
            doc: "What the path is [enum: feature_group, genome_group, aligned_dna_fasta, aligned_protein_fasta, feature_dna_fasta, feature_protein_fasta, feature_ids] [bvbrc:enum]"
    doc: "Sequence data inputs — one record per input. A Feature Group is {filename: <path>, type: feature_group}. [bvbrc:group]"

  alphabet:
    type: string
    doc: "Sequence alphabet [enum: DNA, Protein] [bvbrc:enum]"

  alignment_program:
    type: string?
    doc: "Alignment program [enum: muscle, mafft] [bvbrc:enum]"

  trim_threshold:
    type: float?
    doc: "Alignment end-trimming threshold"

  gap_threshold:
    type: float?
    doc: "Delete gappy sequences threshold"

  substitution_model:
    type: string?
    doc: "Substitution model [enum: HKY85, JC69, K80, F81, F84, TN93, GTR, LG, WAG, JTT, MtREV, Dayhoff, DCMut, RtREV, CpREV, VT, AB, Blosum62, MtMam, MtArt, HIVw, HIVb] [bvbrc:enum]"

  bootstrap:
    type: int?
    doc: "Number of bootstrap replicates"

  recipe:
    type: string?
    doc: "Tree-building method [enum: RAxML, PhyML, FastTree] [bvbrc:enum]"
    default: "RAxML"

  tree_type:
    type: string?
    doc: "Type of tree [enum: viral_genome, gene] [bvbrc:enum]"

  feature_metadata_fields:
    type: string[]?
    doc: "Metadata fields to retrieve for each gene [bvbrc:array]"

  genome_metadata_fields:
    type: string[]?
    doc: "Metadata fields to retrieve for each genome [bvbrc:array]"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  tree_nwk:
    type: File?
    doc: "Phylogenetic tree (Newick)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.nwk"

  tree_phyloxml:
    type: File?
    doc: "Phylogenetic tree (PhyloXML with metadata)"
    outputBinding:
      glob: "$(inputs.output_file)_tree.phyloxml"

  alignment:
    type: File?
    doc: "Multiple sequence alignment (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).afa"

  tree_svg:
    type: File?
    doc: "Tree visualization (SVG)"
    outputBinding:
      glob: "$(inputs.output_file).svg"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
