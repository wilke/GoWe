cwlVersion: v1.2
class: CommandLineTool

doc: "Annotate genome — Calls genes and functionally annotate input contig set."

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hint:
  inject_bvbrc_token: true

requirements:
  InlineJavascriptRequirement: {}
  InitialWorkDirRequirement:
    listing:
      - entryname: params.json
        entry: |
          ${
            var params = {};
            if (inputs.contigs) params.contigs = inputs.contigs;
            if (inputs.scientific_name) params.scientific_name = inputs.scientific_name;
            if (inputs.taxonomy_id) params.taxonomy_id = inputs.taxonomy_id;
            if (inputs.code) params.code = inputs.code;
            if (inputs.domain) params.domain = inputs.domain;
            if (inputs.public !== null && inputs.public !== undefined) params.public = inputs.public;
            if (inputs.queue_nowait !== null && inputs.queue_nowait !== undefined) params.queue_nowait = inputs.queue_nowait;
            if (inputs.skip_indexing !== null && inputs.skip_indexing !== undefined) params.skip_indexing = inputs.skip_indexing;
            if (inputs.skip_workspace_output !== null && inputs.skip_workspace_output !== undefined) params.skip_workspace_output = inputs.skip_workspace_output;
            if (inputs.output_path) params.output_path = inputs.output_path;
            if (inputs.output_file) params.output_file = inputs.output_file;
            if (inputs.reference_genome_id) params.reference_genome_id = inputs.reference_genome_id;
            if (inputs.reference_virus_name) params.reference_virus_name = inputs.reference_virus_name;
            if (inputs.container_id) params.container_id = inputs.container_id;
            if (inputs.indexing_url) params.indexing_url = inputs.indexing_url;
            if (inputs._parent_job) params._parent_job = inputs._parent_job;
            if (inputs.fix_errors !== null && inputs.fix_errors !== undefined) params.fix_errors = inputs.fix_errors;
            if (inputs.fix_frameshifts !== null && inputs.fix_frameshifts !== undefined) params.fix_frameshifts = inputs.fix_frameshifts;
            if (inputs.enable_debug !== null && inputs.enable_debug !== undefined) params.enable_debug = inputs.enable_debug;
            if (inputs.verbose_level) params.verbose_level = inputs.verbose_level;
            if (inputs.workflow) params.workflow = inputs.workflow;
            if (inputs.recipe) params.recipe = inputs.recipe;
            if (inputs.disable_replication !== null && inputs.disable_replication !== undefined) params.disable_replication = inputs.disable_replication;
            if (inputs.analyze_quality !== null && inputs.analyze_quality !== undefined) params.analyze_quality = inputs.analyze_quality;
            if (inputs.custom_pipeline) params.custom_pipeline = inputs.custom_pipeline;
            return JSON.stringify(params);
          }

baseCommand:
  - /home/ac.cucinell/dev_container/bin/App-GenomeAnnotation
  - https://www.bv-brc.org/services/app_service
  - /home/ac.cucinell/dev_container/modules/p3_genome_annotation/app_specs/GenomeAnnotation.json

arguments:
  - params.json

inputs:
  contigs:
    type: string
    doc: "Input set of DNA contigs for annotation [bvbrc:wstype]"
  scientific_name:
    type: string?
    doc: "Scientific name of genome to be annotated"
  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy identfier for this genome"
  code:
    type: string
    doc: "Genetic code used in translation of DNA sequences [enum: 11, 4] [bvbrc:enum]"
    default: 11
  domain:
    type: string
    doc: "Domain of the submitted genome [enum: Bacteria, Archaea] [bvbrc:enum]"
    default: "Bacteria"
  public:
    type: boolean?
    doc: "Make this genome public [bvbrc:bool]"
    default: false
  queue_nowait:
    type: boolean?
    doc: "If set, don't wait for the indexing to finish before marking the job complete. [bvbrc:bool]"
    default: false
  skip_indexing:
    type: boolean?
    doc: "If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. [bvbrc:bool]"
    default: false
  skip_workspace_output:
    type: boolean?
    doc: "If set, don't write anything to workspace. [bvbrc:bool]"
    default: false
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  reference_virus_name:
    type: string?
    doc: "Reference virus name"
  container_id:
    type: string?
    doc: "(Internal) Container to use for this run"
  indexing_url:
    type: string?
    doc: "(Internal) Override Data API URL for use in indexing"
  _parent_job:
    type: string?
    doc: "(Internal) Parent job for this annotation"
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
  workflow:
    type: string?
    doc: "Specifies a custom workflow document (expert)."
  recipe:
    type: string?
    doc: "Specifies a non-default annotation recipe"
  disable_replication:
    type: boolean?
    doc: "Even if this job is identical to a previous job, run it from scratch. [bvbrc:bool]"
  analyze_quality:
    type: boolean?
    doc: "If enabled, run quality analysis on genome [bvbrc:bool]"
  custom_pipeline:
    type: string?
    doc: "Customize the RASTtk pipeline [bvbrc:group]"

outputs:
  genome:
    type: File
    doc: "Annotated Genome Typed Object (GTO, JSON)"
    outputBinding:
      glob: "$(inputs.output_file).genome"
  genbank:
    type: File
    doc: "Genome in GenBank format"
    outputBinding:
      glob: "$(inputs.output_file).gb"
  gff:
    type: File
    doc: "Features in GFF3 format"
    outputBinding:
      glob: "$(inputs.output_file).gff"
  protein_fasta:
    type: File
    doc: "Protein sequences (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).feature_protein.fasta"
  dna_fasta:
    type: File?
    doc: "Feature DNA sequences (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).feature_dna.fasta"
  contigs:
    type: File?
    doc: "Contig sequences (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).contigs.fasta"
  features:
    type: File?
    doc: "Feature table (TSV)"
    outputBinding:
      glob: "$(inputs.output_file).features.txt"
  embl:
    type: File?
    doc: "Genome in EMBL format"
    outputBinding:
      glob: "$(inputs.output_file).embl"
  spreadsheet:
    type: File?
    doc: "Annotation spreadsheet (XLS)"
    outputBinding:
      glob: "$(inputs.output_file).xls"
  quality:
    type: File?
    doc: "Genome quality metrics (JSON)"
    outputBinding:
      glob: "quality.json"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."