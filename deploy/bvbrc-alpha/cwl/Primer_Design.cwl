cwlVersion: v1.2
class: CommandLineTool

label: primer-design

doc: >-
  Primer Design — Use Primer3 to design primers to a given DNA sequence.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: PrimerDesign
    executor: bvbrc

baseCommand: [PrimerDesign]

inputs:
  input_type:
    type: string
    doc: "How to interpret sequence_input [enum: sequence_text, workplace_fasta, database_id] [bvbrc:enum]"

  sequence_input:
    type: string
    doc: "DNA sequence data (text, workspace file path, or database ID per input_type)"

  SEQUENCE_ID:
    type: string?
    doc: "Sequence ID"

  SEQUENCE_TARGET:
    type: string[]?
    doc: "Start/stop of region that primers must flank [bvbrc:array]"

  SEQUENCE_INCLUDED_REGION:
    type: string[]?
    doc: "Region where primers can be picked [bvbrc:array]"

  SEQUENCE_EXCLUDED_REGION:
    type: string[]?
    doc: "Region where primers cannot overlap [bvbrc:array]"

  SEQUENCE_OVERLAP_JUNCTION_LIST:
    type: string[]?
    doc: "Junction overlap positions [bvbrc:array]"

  PRIMER_PICK_INTERNAL_OLIGO:
    type: int?
    doc: "Pick an internal oligo / hybridization probe (1 = yes)"

  PRIMER_PRODUCT_SIZE_RANGE:
    type: string[]?
    doc: "Min, max product size ranges [bvbrc:array]"

  PRIMER_NUM_RETURN:
    type: int?
    doc: "Max number of primer pairs to report"

  PRIMER_MIN_SIZE:
    type: int?
    doc: "Minimum primer length"

  PRIMER_OPT_SIZE:
    type: int?
    doc: "Optimal primer length"

  PRIMER_MAX_SIZE:
    type: int?
    doc: "Maximum primer length"

  PRIMER_MIN_TM:
    type: float?
    doc: "Minimum primer melting temperature"

  PRIMER_OPT_TM:
    type: float?
    doc: "Optimal primer melting temperature"

  PRIMER_MAX_TM:
    type: float?
    doc: "Maximum primer melting temperature"

  PRIMER_PAIR_MAX_DIFF_TM:
    type: float?
    doc: "Max Tm difference between paired primers"

  PRIMER_MIN_GC:
    type: float?
    doc: "Minimum primer GC percentage"

  PRIMER_OPT_GC:
    type: float?
    doc: "Optimal primer GC percentage"

  PRIMER_MAX_GC:
    type: float?
    doc: "Maximum primer GC percentage"

  PRIMER_SALT_MONOVALENT:
    type: float?
    doc: "Concentration of monovalent cations (mM)"

  PRIMER_SALT_DIVALENT:
    type: float?
    doc: "Concentration of divalent cations (mM)"

  PRIMER_DNA_CONC:
    type: float?
    doc: "Annealing oligo concentration (nM)"

  PRIMER_DNTP_CONC:
    type: float?
    doc: "Concentration of dNTPs (mM)"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  primer_results:
    type: File?
    doc: "Primer3 design results"
    outputBinding:
      glob: "$(inputs.output_file)_primer_results.txt"

  primer_table:
    type: File?
    doc: "Primer pairs summary table"
    outputBinding:
      glob: "$(inputs.output_file)_primers.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
