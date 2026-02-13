cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate genome — Calls genes and functionally annotate input contig set."

hints:
  goweHint:
    bvbrc_app_id: GenomeAnnotationGenbankTest
    executor: bvbrc

baseCommand: [GenomeAnnotationGenbankTest]

inputs:
  genbank_file:
    type: File
    doc: "Genome to process [bvbrc:wstype]"
  public:
    type: boolean?
    doc: "Make this genome public [bvbrc:bool]"
    default: false
  queue_nowait:
    type: boolean?
    doc: "If set, don't wait for the indexing to finish before marking the job complete. [bvbrc:bool]"
    default: false
  output_path:
    type: Directory?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  fix_errors:
    type: boolean?
    doc: "The automatic annotation process may run into problems, such as gene candidates overlapping RNAs, or genes embedded inside other genes. To automatically resolve these problems (even if that requires deleting some gene candidates), enable this option. [bvbrc:bool]"
  fix_frameshifts:
    type: boolean?
    doc: "If you wish for the pipeline to fix frameshifts, enable this option. Otherwise frameshifts will not be corrected. [bvbrc:bool]"
  enable_debug:
    type: boolean?
    doc: "If you wish debug statements to be printed for this job, enable this option. [bvbrc:bool]"
  verbose_level:
    type: int?
    doc: "Set this to the verbosity level of choice for error messages."
  disable_replication:
    type: boolean?
    doc: "Even if this job is identical to a previous job, run it from scratch. [bvbrc:bool]"
  custom_pipeline:
    type: string?
    doc: "Customize the RASTtk pipeline [bvbrc:group]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
