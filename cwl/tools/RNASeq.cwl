cwlVersion: v1.2
class: CommandLineTool

doc: "Analyze RNASeq reads — Align or assemble RNASeq reads into transcripts with normalized expression levels"

hints:
  goweHint:
    bvbrc_app_id: RNASeq
    executor: bvbrc

baseCommand: [RNASeq]

inputs:
  experimental_conditions:
    type: string?
    doc: "Experimental conditions"
  contrasts:
    type: string?
    doc: "Contrast list"
  strand_specific:
    type: boolean?
    doc: "Are the reads in this study strand-specific?"
    default: true
  paired_end_libs:
    type: string?
  single_end_libs:
    type: string?
  srr_libs:
    type: string?
  reference_genome_id:
    type: string
    doc: "Reference genome ID"
  genome_type:
    type: string
    doc: "genome is type bacteria or host"
  recipe:
    type: string
    doc: "Recipe used for RNAseq analysis"
    default: "HTSeq-DESeq"
  host_ftp:
    type: string?
    doc: "Host FTP prefix for obtaining files"
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data."
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  trimming:
    type: boolean?
    doc: "run trimgalore on the reads"
    default: false
  unit_test:
    type: string?
    doc: "Path to the json file used for unit testing"
  skip_sampling:
    type: string?
    doc: "flag to skip the sampling step in alignment.py"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
