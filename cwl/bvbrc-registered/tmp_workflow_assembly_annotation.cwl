$graph:
  - id: GenomeAssembly2
    class: CommandLineTool

    doc: "Assemble WGS reads — Assemble reads into a set of contigs"

    baseCommand:
      - App-GenomeAssembly2
      - https://www.bv-brc.org/services/app_service
      - /opt/p3/deployment/services/app_service/app_specs/GenomeAssembly2.json

    hints:
      inject_bvbrc_token: true

    requirements:
      InlineJavascriptRequirement: {}

      InitialWorkDirRequirement:
        listing:
          - entryname: params.json
            entry: |
              ${
                var params = {};

                if (inputs.paired_end_libs) params.paired_end_libs = inputs.paired_end_libs;
                if (inputs.single_end_libs) params.single_end_libs = inputs.single_end_libs;
                if (inputs.recipe) params.recipe = inputs.recipe;

                if (inputs.racon_iter !== null && inputs.racon_iter !== undefined)
                  params.racon_iter = inputs.racon_iter;
                if (inputs.pilon_iter !== null && inputs.pilon_iter !== undefined)
                  params.pilon_iter = inputs.pilon_iter;
                if (inputs.trim !== null && inputs.trim !== undefined)
                  params.trim = inputs.trim;
                if (inputs.min_contig_len !== null && inputs.min_contig_len !== undefined)
                  params.min_contig_len = inputs.min_contig_len;
                if (inputs.min_contig_cov !== null && inputs.min_contig_cov !== undefined)
                  params.min_contig_cov = inputs.min_contig_cov;

                if (inputs.genome_size) params.genome_size = inputs.genome_size;

                if (inputs.output_path) params.output_path = inputs.output_path;
                if (inputs.output_file) params.output_file = inputs.output_file;

                if (inputs.debug_level !== null && inputs.debug_level !== undefined)
                  params.debug_level = inputs.debug_level;

                if (inputs.container_id) params.container_id = inputs.container_id;
                if (inputs._parent_job) params._parent_job = inputs._parent_job;
                if (inputs.disable_replication !== null && inputs.disable_replication !== undefined)
                  params.disable_replication = inputs.disable_replication;
                if (inputs.skip_workspace_output !== null && inputs.skip_workspace_output !== undefined)
                  params.skip_workspace_output = inputs.skip_workspace_output;

                return JSON.stringify(params);
              }

    arguments:
      - params.json

    inputs:
      paired_end_libs:
        type:
          - "null"
          - type: array
            items:
              name: paired_end_lib
              type: record
              fields:
                - name: read1
                  type: File
                  doc: "Forward reads"

                - name: read2
                  type: File?
                  doc: "Reverse reads"

                - name: platform
                  type: string?
                  default: "infer"
                  doc: "Sequencing platform"

                - name: interleaved
                  type: boolean
                  default: false
                  doc: "Reads are interleaved"

                - name: read_orientation_outward
                  type: boolean?
                  default: false
                  doc: "Reads face outward (mate-pair)"

        doc: "Paired-end read libraries [bvbrc:group]"

      single_end_libs:
        type:
          - "null"
          - type: array
            items:
              name: single_end_lib
              type: record
              fields:
                - name: read
                  type: File
                  doc: "Read file"

                - name: platform
                  type: string?
                  default: "infer"
                  doc: "Sequencing platform"

        doc: "Single-end read libraries [bvbrc:group]"

      recipe:
        type: string?
        default: "auto"
        doc: "Assembly recipe"

      trim:
        type: boolean?
        default: false
        doc: "Trim reads before assembly"

      min_contig_len:
        type: int?
        default: 300
        doc: "Minimum contig length filter"

      min_contig_cov:
        type: float?
        default: 5
        doc: "Minimum contig coverage filter"

      genome_size:
        type: int?
        default: 5000000
        doc: "Estimated genome size"

      pilon_iter:
        type: int?
        default: 2
        doc: "Pilon polishing iterations"

      racon_iter:
        type: int?
        default: 2
        doc: "Racon polishing iterations"

      debug_level:
        type: int?
        default: 0
        doc: "Debugging level"

      container_id:
        type: string?
        doc: "Internal container to use for this run"

      _parent_job:
        type: string?
        doc: "Parent job for this assembly"

      disable_replication:
        type: boolean?
        default: false
        doc: "Run from scratch even if an identical job exists [bvbrc:bool]"

      skip_workspace_output:
        type: boolean?
        default: false
        doc: "Do not write assembly output to the workspace"

      output_path:
        type: string
        doc: "Workspace output folder [bvbrc:folder]"

      output_file:
        type: string
        doc: "Output basename [bvbrc:wsid]"

    outputs:
      contigs:
        type: string
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + "_contigs.fasta")
        doc: "Workspace path to the assembled contigs"

      report:
        type: string?
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + "_AssemblyReport.html")
        doc: "Workspace path to the assembly report"

      run_details:
        type: string?
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + "_run_details.json")
        doc: "Workspace path to the assembly run details"

      result_folder:
        type: string?
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file)
        doc: "Workspace path to the full assembly output folder"

  - id: GenomeAnnotation
    class: CommandLineTool

    doc: "Annotate genome — Calls genes and functionally annotates an input contig set."

    baseCommand:
      - /home/ac.cucinell/dev_container/bin/App-GenomeAnnotation
      - https://www.bv-brc.org/services/app_service
      - /home/ac.cucinell/dev_container/modules/p3_genome_annotation/app_specs/GenomeAnnotation.json

    hints:
      inject_bvbrc_token: true

    requirements:
      InlineJavascriptRequirement: {}

      InitialWorkDirRequirement:
        listing:
          - entryname: params.json
            entry: |
              ${
                var params = {};

                if (inputs.contigs) {
                  params.contigs = inputs.contigs;
                }

                if (inputs.scientific_name) {
                  params.scientific_name = inputs.scientific_name;
                }

                if (
                  inputs.taxonomy_id !== null &&
                  inputs.taxonomy_id !== undefined
                ) {
                  params.taxonomy_id = inputs.taxonomy_id;
                }

                if (inputs.code) {
                  params.code = inputs.code;
                }

                if (inputs.domain) {
                  params.domain = inputs.domain;
                }

                if (
                  inputs.public !== null &&
                  inputs.public !== undefined
                ) {
                  params.public = inputs.public;
                }

                if (
                  inputs.queue_nowait !== null &&
                  inputs.queue_nowait !== undefined
                ) {
                  params.queue_nowait = inputs.queue_nowait;
                }

                if (
                  inputs.skip_indexing !== null &&
                  inputs.skip_indexing !== undefined
                ) {
                  params.skip_indexing = inputs.skip_indexing;
                }

                if (
                  inputs.skip_workspace_output !== null &&
                  inputs.skip_workspace_output !== undefined
                ) {
                  params.skip_workspace_output =
                    inputs.skip_workspace_output;
                }

                if (inputs.output_path) {
                  params.output_path = inputs.output_path;
                }

                if (inputs.output_file) {
                  params.output_file = inputs.output_file;
                }

                if (inputs.reference_genome_id) {
                  params.reference_genome_id =
                    inputs.reference_genome_id;
                }

                if (inputs.reference_virus_name) {
                  params.reference_virus_name =
                    inputs.reference_virus_name;
                }

                if (inputs.container_id) {
                  params.container_id = inputs.container_id;
                }

                if (inputs.indexing_url) {
                  params.indexing_url = inputs.indexing_url;
                }

                if (inputs._parent_job) {
                  params._parent_job = inputs._parent_job;
                }

                if (
                  inputs.fix_errors !== null &&
                  inputs.fix_errors !== undefined
                ) {
                  params.fix_errors = inputs.fix_errors;
                }

                if (
                  inputs.fix_frameshifts !== null &&
                  inputs.fix_frameshifts !== undefined
                ) {
                  params.fix_frameshifts = inputs.fix_frameshifts;
                }

                if (
                  inputs.enable_debug !== null &&
                  inputs.enable_debug !== undefined
                ) {
                  params.enable_debug = inputs.enable_debug;
                }

                if (
                  inputs.verbose_level !== null &&
                  inputs.verbose_level !== undefined
                ) {
                  params.verbose_level = inputs.verbose_level;
                }

                if (inputs.workflow) {
                  params.workflow = inputs.workflow;
                }

                if (inputs.recipe) {
                  params.recipe = inputs.recipe;
                }

                if (
                  inputs.disable_replication !== null &&
                  inputs.disable_replication !== undefined
                ) {
                  params.disable_replication =
                    inputs.disable_replication;
                }

                if (
                  inputs.analyze_quality !== null &&
                  inputs.analyze_quality !== undefined
                ) {
                  params.analyze_quality = inputs.analyze_quality;
                }

                if (inputs.custom_pipeline) {
                  params.custom_pipeline = inputs.custom_pipeline;
                }

                return JSON.stringify(params);
              }

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
        doc: "NCBI Taxonomy identifier for this genome"

      code:
        type: string
        default: "11"
        doc: "Genetic code used in translation [enum: 11, 4] [bvbrc:enum]"

      domain:
        type: string
        default: "Bacteria"
        doc: "Domain of the submitted genome [enum: Bacteria, Archaea] [bvbrc:enum]"

      public:
        type: boolean?
        default: false
        doc: "Make this genome public [bvbrc:bool]"

      queue_nowait:
        type: boolean?
        default: false
        doc: "Do not wait for indexing before marking the job complete"

      skip_indexing:
        type: boolean?
        default: false
        doc: "Do not index this genome in Solr"

      skip_workspace_output:
        type: boolean?
        default: false
        doc: "Do not write annotation output to the workspace"

      output_path:
        type: string
        doc: "Workspace output folder [bvbrc:folder]"

      output_file:
        type: string
        doc: "Output basename [bvbrc:wsid]"

      reference_genome_id:
        type: string?
        doc: "Reference genome ID"

      reference_virus_name:
        type: string?
        doc: "Reference virus name"

      container_id:
        type: string?
        doc: "Internal container to use for this run"

      indexing_url:
        type: string?
        doc: "Override Data API URL used for indexing"

      _parent_job:
        type: string?
        doc: "Parent job for this annotation"

      fix_errors:
        type: boolean?
        doc: "Automatically resolve annotation errors"

      fix_frameshifts:
        type: boolean?
        doc: "Correct frameshifts during annotation"

      enable_debug:
        type: boolean?
        doc: "Enable annotation debugging output"

      verbose_level:
        type: int?
        doc: "Annotation error-message verbosity level"

      workflow:
        type: string?
        doc: "Custom annotation workflow document"

      recipe:
        type: string?
        doc: "Non-default annotation recipe"

      disable_replication:
        type: boolean?
        doc: "Run from scratch even if an identical job exists"

      analyze_quality:
        type: boolean?
        doc: "Run genome quality analysis"

      custom_pipeline:
        type: string?
        doc: "Customize the RASTtk pipeline"

    outputs:
      genome:
        type: string?
        doc: "Workspace path to the annotated Genome Typed Object"

      genbank:
        type: string?
        doc: "Workspace path to the genome in GenBank format"

      gff:
        type: string?
        doc: "Workspace path to the GFF3 feature file"

      protein_fasta:
        type: string?
        doc: "Workspace path to the protein FASTA file"

      dna_fasta:
        type: string?
        doc: "Workspace path to the feature DNA FASTA file"

      contigs:
        type: string?
        doc: "Workspace path to the annotation contig FASTA file"

      features:
        type: string?
        doc: "Workspace path to the annotation feature table"

      embl:
        type: string?
        doc: "Workspace path to the EMBL output"

      spreadsheet:
        type: string?
        doc: "Workspace path to the annotation spreadsheet"

      quality:
        type: string?
        doc: "Workspace path to the genome quality metrics"

      result_folder:
        type: string?
        doc: "Workspace path to the full annotation output folder"

  - id: main
    class: Workflow

    label: AssemblyAnnotation
    doc: "Assemble WGS reads then annotate the resulting contigs"

    requirements:
      InlineJavascriptRequirement: {}
      StepInputExpressionRequirement: {}

    inputs:
      paired_end_libs:
        type:
          - "null"
          - type: array
            items:
              name: paired_end_lib
              type: record
              fields:
                - name: read1
                  type: File
                  doc: "Forward reads"

                - name: read2
                  type: File?
                  doc: "Reverse reads"

                - name: platform
                  type: string?
                  default: "infer"
                  doc: "Sequencing platform"

                - name: interleaved
                  type: boolean
                  default: false
                  doc: "Reads are interleaved"

                - name: read_orientation_outward
                  type: boolean?
                  default: false
                  doc: "Reads face outward (mate-pair)"

        doc: "Paired-end read libraries [bvbrc:group]"

      single_end_libs:
        type:
          - "null"
          - type: array
            items:
              name: single_end_lib
              type: record
              fields:
                - name: read
                  type: File
                  doc: "Read file"

                - name: platform
                  type: string?
                  default: "infer"
                  doc: "Sequencing platform"

        doc: "Single-end read libraries"

      assembly_recipe:
        type: string?
        default: "auto"
        doc: "Assembly recipe"

      trim:
        type: boolean?
        default: false
        doc: "Trim reads before assembly"

      min_contig_len:
        type: int?
        default: 300
        doc: "Minimum contig length filter"

      min_contig_cov:
        type: float?
        default: 5
        doc: "Minimum contig coverage filter"

      genome_size:
        type: int?
        default: 5000000
        doc: "Estimated genome size"

      pilon_iter:
        type: int?
        default: 2
        doc: "Pilon polishing iterations"

      racon_iter:
        type: int?
        default: 2
        doc: "Racon polishing iterations"

      scientific_name:
        type: string?
        doc: "Scientific name for annotation"

      taxonomy_id:
        type: int?
        doc: "NCBI Taxonomy ID"

      domain:
        type: string
        default: "Bacteria"
        doc: "Domain [enum: Bacteria, Archaea]"

      code:
        type: string
        default: "11"
        doc: "Genetic code [enum: 11, 4]"

      public:
        type: boolean?
        default: false
        doc: "Make this genome public"

      queue_nowait:
        type: boolean?
        default: false
        doc: "Do not wait for indexing to finish"

      skip_indexing:
        type: boolean?
        default: false
        doc: "Do not index this genome in Solr"

      skip_workspace_output:
        type: boolean?
        default: false
        doc: "Do not write annotation output to the workspace"

      reference_genome_id:
        type: string?
        doc: "Reference genome ID"

      reference_virus_name:
        type: string?
        doc: "Reference virus name"

      container_id:
        type: string?
        doc: "Internal annotation container"

      indexing_url:
        type: string?
        doc: "Override annotation indexing URL"

      _parent_job:
        type: string?
        doc: "Parent annotation job"

      fix_errors:
        type: boolean?
        doc: "Automatically resolve annotation errors"

      fix_frameshifts:
        type: boolean?
        doc: "Correct frameshifts"

      enable_debug:
        type: boolean?
        doc: "Enable debugging output"

      verbose_level:
        type: int?
        doc: "Annotation verbosity level"

      annotation_workflow:
        type: string?
        doc: "Custom annotation workflow document"

      annotation_recipe:
        type: string?
        doc: "Non-default annotation recipe"

      disable_replication:
        type: boolean?
        doc: "Run annotation from scratch"

      analyze_quality:
        type: boolean?
        doc: "Run genome quality analysis"

      custom_pipeline:
        type: string?
        doc: "Customize the RASTtk pipeline"

      output_path:
        type: string
        doc: "Workspace output folder [bvbrc:folder]"

      output_file:
        type: string
        doc: "Output basename [bvbrc:wsid]"

    outputs:
      contigs:
        type: string?
        outputSource: assemble/contigs

      assembly_report:
        type: string?
        outputSource: assemble/report

      assembly_run_details:
        type: string?
        outputSource: assemble/run_details

      assembly_result_folder:
        type: string?
        outputSource: assemble/result_folder

      genome:
        type: string?
        outputSource: annotate/genome

      genbank:
        type: string?
        outputSource: annotate/genbank

      gff:
        type: string?
        outputSource: annotate/gff

      protein_fasta:
        type: string?
        outputSource: annotate/protein_fasta

      dna_fasta:
        type: string?
        outputSource: annotate/dna_fasta

      annotation_contigs:
        type: string?
        outputSource: annotate/contigs

      features:
        type: string?
        outputSource: annotate/features

      embl:
        type: string?
        outputSource: annotate/embl

      spreadsheet:
        type: string?
        outputSource: annotate/spreadsheet

      quality:
        type: string?
        outputSource: annotate/quality

      result_folder:
        type: string?
        outputSource: annotate/result_folder

    steps:
      assemble:
        in:
          paired_end_libs: paired_end_libs
          single_end_libs: single_end_libs
          recipe: assembly_recipe
          trim: trim
          min_contig_len: min_contig_len
          min_contig_cov: min_contig_cov
          genome_size: genome_size
          pilon_iter: pilon_iter
          racon_iter: racon_iter
          output_path: output_path

          output_file:
            source: output_file
            valueFrom: $(self + "_assembly")

        out:
          - contigs
          - report
          - run_details
          - result_folder

        run: "#GenomeAssembly2"

      annotate:
        in:
          contigs: assemble/contigs
          scientific_name: scientific_name
          taxonomy_id: taxonomy_id
          domain: domain
          code: code
          public: public
          queue_nowait: queue_nowait
          skip_indexing: skip_indexing
          skip_workspace_output: skip_workspace_output
          reference_genome_id: reference_genome_id
          reference_virus_name: reference_virus_name
          container_id: container_id
          indexing_url: indexing_url
          _parent_job: _parent_job
          fix_errors: fix_errors
          fix_frameshifts: fix_frameshifts
          enable_debug: enable_debug
          verbose_level: verbose_level
          workflow: annotation_workflow
          recipe: annotation_recipe
          disable_replication: disable_replication
          analyze_quality: analyze_quality
          custom_pipeline: custom_pipeline
          output_path: output_path

          output_file:
            source: output_file
            valueFrom: $(self + "_annotation")

        out:
          - genome
          - genbank
          - gff
          - protein_fasta
          - dna_fasta
          - contigs
          - features
          - embl
          - spreadsheet
          - quality
          - result_folder

        run: "#GenomeAnnotation"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

cwlVersion: v1.2
