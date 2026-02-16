cwlVersion: v1.2
class: CommandLineTool

doc: "Primer Design — Use Primer3 to design primers to given sequence"

hints:
  goweHint:
    bvbrc_app_id: PrimerDesign
    executor: bvbrc

baseCommand: [PrimerDesign]

inputs:
  input_type:
    type: string
    doc: "Input type [enum: sequence_text, workplace_fasta, database_id] [bvbrc:enum]"
  sequence_input:
    type: string
    doc: "DNA sequence data"
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
    doc: "Junction overlap list [bvbrc:array]"
  PRIMER_PICK_INTERNAL_OLIGO:
    type: int?
    doc: "Pick internal oligo (1=yes)"
  PRIMER_PRODUCT_SIZE_RANGE:
    type: string[]?
    doc: "Min, max product size [bvbrc:array]"
  PRIMER_NUM_RETURN:
    type: int?
    doc: "Max num primer pairs to report [bvbrc:integer]"
  PRIMER_MIN_SIZE:
    type: int?
    doc: "Min primer length [bvbrc:integer]"
  PRIMER_OPT_SIZE:
    type: int?
    doc: "Optimal primer length [bvbrc:integer]"
  PRIMER_MAX_SIZE:
    type: int?
    doc: "Maximum primer length [bvbrc:integer]"
  PRIMER_MAX_TM:
    type: float?
    doc: "Maximum primer melting temperature [bvbrc:number]"
  PRIMER_MIN_TM:
    type: float?
    doc: "Minimum primer melting temperature [bvbrc:number]"
  PRIMER_OPT_TM:
    type: float?
    doc: "Optimal primer melting temperature [bvbrc:number]"
  PRIMER_PAIR_MAX_DIFF_TM:
    type: float?
    doc: "Max Tm difference of paired primers [bvbrc:number]"
  PRIMER_MAX_GC:
    type: float?
    doc: "Maximum primer GC percentage [bvbrc:number]"
  PRIMER_MIN_GC:
    type: float?
    doc: "Minimum primer GC percentage [bvbrc:number]"
  PRIMER_OPT_GC:
    type: float?
    doc: "Optimal primer GC percentage [bvbrc:number]"
  PRIMER_SALT_MONOVALENT:
    type: float?
    doc: "Concentration of monovalent cations (mM) [bvbrc:number]"
  PRIMER_SALT_DIVALENT:
    type: float?
    doc: "Concentration of divalent cations (mM) [bvbrc:number]"
  PRIMER_DNA_CONC:
    type: float?
    doc: "Annealing Oligo Concentration (nM) [bvbrc:number]"
  PRIMER_DNTP_CONC:
    type: float?
    doc: "Concentration of dNTPs (mM) [bvbrc:number]"
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
