cwlVersion: v1.2
class: CommandLineTool

label: metacats

doc: >-
  Metadata-driven Comparative Analysis Tool (meta-CATS) — Identify
  positions that significantly differ between user-defined groups
  of sequences.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: MetaCATS
    executor: bvbrc

baseCommand: [MetaCATS]

inputs:
  input_type:
    type: string
    doc: "Data entry input type [enum: auto, groups, files] [bvbrc:enum]"

  alphabet:
    type: string
    doc: "Sequence alphabet [enum: na, aa] [bvbrc:enum]"
    default: "na"

  p_value:
    type: float
    doc: "P-value cutoff for significance"
    default: 0.05

  groups:
    type: string[]?
    doc: "Feature group workspace paths [bvbrc:list]"

  alignment_file:
    type: File?
    doc: "The location of the alignment file. [bvbrc:wstype]"

  group_file:
    type: File?
    doc: "The location of a file that partitions sequences into groups. [bvbrc:wstype]"

  alignment_type:
    type: string?
    doc: "Alignment file format [enum: aligned_dna_fasta, aligned_protein_fasta] [bvbrc:enum]"

  auto_groups:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: auto_group
          fields:
            - name: id
              type: string
              doc: "Feature / PATRIC ID"
            - name: grp
              type: string
              doc: "Group assignment string"
            - name: g_id
              type: string?
              doc: "Genome ID"
            - name: metadata
              type: string?
              doc: "Original metadata category"
    doc: "Auto-generated sequence groups [bvbrc:group]"

  year_ranges:
    type: string?
    doc: "Year ranges for temporal grouping"

  metadata_group:
    type: string?
    doc: "Metadata type used for reference"

  output_path:
    type: Directory
    doc: "Path to which the output will be written. [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files. [bvbrc:wsid]"

outputs:
  significant_positions:
    type: File?
    doc: "Significant positions report (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_significant_positions.tsv"

  chi_square_results:
    type: File?
    doc: "Chi-square test results"
    outputBinding:
      glob: "$(inputs.output_file)_chi_square.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
