cwlVersion: v1.2
class: CommandLineTool

doc: "Primer Design — Use Primer3 to design primers to given sequence"

hints:
  goweHint:
    bvbrc_app_id: PrimerDesign
    executor: bvbrc

baseCommand: [PrimerDesign]

inputs:
  output_file:
    type: string
    doc: "Basename for the generated output files."
  output_path:
    type: string
    doc: "Path to which the output will be written."
  SEQUENCE_ID:
    type: string
    doc: "Sequence ID"
  SEQUENCE_TEMPLATE:
    type: string
    doc: "Nucleotide Sequence or (BVBRC Seq Id)"
  SEQUENCE_TARGET:
    type: string?
    doc: "Start/stop of region that primers must flank"
  SEQUENCE_INCLUDED_REGION:
    type: string?
    doc: "Region where primers can be picked"
  SEQUENCE_EXCLUDED_REGION:
    type: string?
    doc: "Region where primers cannot overlap"
  SEQUENCE_OVERLAP_JUNCTION_LIST:
    type: string?
    doc: "Start position and length of region that primers must flank"
  PRIMER_PRODUCT_SIZE_RANGE:
    type: string?
    doc: "Min, max product size"
  PRIMER_NUM_RETURN:
    type: int?
    doc: "Max num primer pairs to report"
  PRIMER_MIN_SIZE:
    type: int?
    doc: "Min primer length"
  PRIMER_OPT_SIZE:
    type: int?
    doc: "Optimal primer length"
  PRIMER_MAX_SIZE:
    type: int?
    doc: "Maximum primer length"
  PRIMER_MAX_TM:
    type: float?
    doc: "Maximum primer melting temperature"
  PRIMER_MIN_TM:
    type: float?
    doc: "Minimum primer melting temperature"
  PRIMER_OPT_TM:
    type: float?
    doc: "Optimal primer melting temperature"
  PRIMER_PAIR_MAX_DIFF_TM:
    type: float?
    doc: "Max Tm difference of paired primers"
  PRIMER_MAX_GC:
    type: float?
    doc: "Maximum primer GC percentage"
  PRIMER_MIN_GC:
    type: float?
    doc: "Minimum primer GC percentage"
  PRIMER_OPT_GC:
    type: float?
    doc: "Optimal primer GC percentage"
  PRIMER_SALT_MONOVALENT:
    type: float?
    doc: "Concentration of monovalent cations (mM)"
  PRIMER_SALT_DIVALENT:
    type: float?
    doc: "Concentration of divalent cations (mM)"
  PRIMER_DNA_CONC:
    type: float?
    doc: "Annealing Oligo Concentration (nM)"
  PRIMER_DNTP_CONC:
    type: float?
    doc: "Concentration of dNTPs (mM)"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
