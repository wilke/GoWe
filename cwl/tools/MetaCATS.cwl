cwlVersion: v1.2
class: CommandLineTool

doc: "Metadata-driven Comparative Analysis Tool (meta-CATS) — The meta-CATS tool looks for positions that significantly differ between user-defined groups of sequences."

hints:
  goweHint:
    bvbrc_app_id: MetaCATS
    executor: bvbrc

baseCommand: [MetaCATS]

inputs:
  p_value:
    type: float
    doc: "P-value cutoff"
    default: 0.05
  year_ranges:
    type: string?
    doc: "Year ranges"
  metadata_group:
    type: string?
    doc: "Metadata type"
  input_type:
    type: string
    doc: "Input type [enum: auto, groups, files] [bvbrc:enum]"
  alphabet:
    type: string
    doc: "Sequence alphabet [enum: na, aa] [bvbrc:enum]"
    default: "na"
  groups:
    type: string[]?
    doc: "Feature groups [bvbrc:list]"
  alignment_file:
    type: File?
    doc: "Alignment file [bvbrc:wstype]"
  group_file:
    type: File?
    doc: "Group file [bvbrc:wstype]"
  alignment_type:
    type: string?
    doc: "Alignment type [enum: aligned_dna_fasta, aligned_protein_fasta] [bvbrc:enum]"
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
              doc: "Sequence ID"
            - name: grp
              type: string
              doc: "Group assignment"
            - name: g_id
              type: string
              doc: "Genome ID"
            - name: metadata
              type: string
              doc: "Metadata value"
    doc: " [bvbrc:group]"
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
