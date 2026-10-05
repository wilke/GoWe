cwlVersion: v1.2
class: CommandLineTool

label: tnseq-analysis

doc: >-
  TnSeq Analysis — Use TRANSIT to analyze transposon insertion
  sequencing data for essential gene identification.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: TnSeq
    executor: bvbrc

baseCommand: [TnSeq]

inputs:
  reference_genome_id:
    type: string
    doc: "Reference genome ID"

  recipe:
    type: string?
    doc: "TRANSIT analysis method [enum: gumbel, griffin, tn5gaps, rankproduct, hmm, binomial, resampling] [bvbrc:enum]"
    default: "gumbel"

  protocol:
    type: string?
    doc: "TnSeq protocol [enum: sassetti, tn5, mme1] [bvbrc:enum]"
    default: "sassetti"

  experimental_conditions:
    type: string[]?
    doc: "Experimental condition labels [bvbrc:array]"

  contrasts:
    type: string[]?
    doc: "Contrasts between conditions [bvbrc:array]"

  read_files:
    type: string?
    doc: "Read files [bvbrc:group]"

  primer:
    type: string?
    doc: "Primer DNA string for read trimming"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  essentiality_results:
    type: File?
    doc: "Gene essentiality calls"
    outputBinding:
      glob: "$(inputs.output_file)_essentiality.tsv"

  insertion_counts:
    type: File?
    doc: "Transposon insertion counts per gene"
    outputBinding:
      glob: "$(inputs.output_file)_counts.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
