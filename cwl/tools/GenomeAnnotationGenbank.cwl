cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate genome — Calls genes and functionally annotate input contig set."

hints:
  goweHint:
    bvbrc_app_id: GenomeAnnotationGenbank
    executor: bvbrc

baseCommand: [GenomeAnnotationGenbank]

inputs:
  genbank_file:
    type: string
    doc: "Genome to process"
  public:
    type: boolean?
    doc: "Make this genome public"
    default: false
  queue_nowait:
    type: boolean?
    doc: "If set, don't wait for the indexing to finish before marking the job complete."
    default: false
  skip_indexing:
    type: boolean?
    doc: "If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site."
    default: false
  skip_workspace_output:
    type: boolean?
    doc: "If set, don't write anything to workspace."
    default: false
  container_id:
    type: string?
    doc: "(Internal) Container to use for this run"
  indexing_url:
    type: string?
    doc: "(Internal) Override Data API URL for use in indexing"
  output_path:
    type: Directory?
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. "
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  reference_virus_name:
    type: string?
    doc: "Reference virus name"
  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)."
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe"
  scientific_name:
    type: string?
    doc: "Scientific name of genome to be annotated. Overrides value in genbank file."
  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy identfier for this genome. Overrides value in genbank file."
  code:
    type: string?
    doc: "Genetic code used in translation of DNA sequences. Overrides value in genbank file."
  domain:
    type: string?
    doc: "Domain of the submitted genome. Overrides value in genbank file."
    default: "Bacteria"
  import_only:
    type: boolean?
    doc: "Import this genome (do not reannotate gene calls or protein functions)"
  fix_errors:
    type: boolean?
    doc: "The automatic annotation process may run into problems, such as gene candidates overlapping RNAs, or genes embedded inside other genes. To automatically resolve these problems (even if that requires deleting some gene candidates), enable this option."
  fix_frameshifts:
    type: boolean?
    doc: "If you wish for the pipeline to fix frameshifts, enable this option. Otherwise frameshifts will not be corrected."
  enable_debug:
    type: boolean?
    doc: "If you wish debug statements to be printed for this job, enable this option."
  verbose_level:
    type: int?
    doc: "Set this to the verbosity level of choice for error messages."
  disable_replication:
    type: boolean?
    doc: "Even if this job is identical to a previous job, run it from scratch."
  custom_pipeline:
    type: string?
    doc: "Customize the RASTtk pipeline"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
