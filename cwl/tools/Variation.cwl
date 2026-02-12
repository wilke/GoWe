cwlVersion: v1.2
class: CommandLineTool

doc: "Identify SNVs — Identify and annotate small nucleotide variations relative to a reference genome"

hints:
  goweHint:
    bvbrc_app_id: Variation
    executor: bvbrc

baseCommand: [Variation]

inputs:
  reference_genome_id:
    type: string
    doc: "Reference genome ID"
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  mapper:
    type: string?
    doc: "Tool used for mapping short reads against the reference genome"
    default: "BWA-mem"
  caller:
    type: string?
    doc: "Tool used for calling variations based on short read mapping"
    default: "FreeBayes"
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data."
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
