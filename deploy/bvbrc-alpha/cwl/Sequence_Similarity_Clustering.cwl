$graph:
  - id: SSC
    class: CommandLineTool

    doc: "Sequence Similarity Clustering — Cluster sequences from a FASTA/FASTQ file using MMseqs2 and produce a FASTA file with cluster-labeled headers."

    baseCommand:
      - /home/ac.cucinell/dev_container/bin/App-SSC
      - https://www.bv-brc.org/services/app_service
      - /home/ac.cucinell/dev_container/modules/sequence-similarity-clustering/app_specs/SSC.json

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

                if (inputs.input_file) params.input_file = inputs.input_file;

                if (inputs.identity !== null && inputs.identity !== undefined)
                  params.identity = inputs.identity;
                if (inputs.coverage !== null && inputs.coverage !== undefined)
                  params.coverage = inputs.coverage;
                if (inputs.cluster_mode !== null && inputs.cluster_mode !== undefined)
                  params.cluster_mode = inputs.cluster_mode;

                if (inputs.output_path) params.output_path = inputs.output_path;
                if (inputs.output_file) params.output_file = inputs.output_file;

                return JSON.stringify(params);
              }

    arguments:
      - params.json

    inputs:
      input_file:
        type: string
        doc: "Workspace path to the input FASTA or FASTQ file [bvbrc:wstype]"

      identity:
        type: float?
        default: 0.95
        doc: "Minimum sequence identity (0.0-1.0) for clustering"

      coverage:
        type: float?
        default: 0.8
        doc: "Minimum coverage fraction of the shorter sequence (0.0-1.0)"

      cluster_mode:
        type: int?
        default: 0
        doc: "Clustering algorithm [enum: 0, 1, 2] [bvbrc:enum]"

      output_path:
        type: string
        doc: "Workspace output folder [bvbrc:folder]"

      output_file:
        type: string
        doc: "Output basename [bvbrc:wsid]"

    outputs:
      clustered_fasta:
        type: string
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + ".fasta")
        doc: "Workspace path to the clustered FASTA output"

      cluster_report:
        type: string
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + "_clusters.tsv")
        doc: "Workspace path to the cluster membership report"

      summary:
        type: string
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file + "/" + inputs.output_file + "_summary.json")
        doc: "Workspace path to the clustering summary"

      result_folder:
        type: string?
        outputBinding:
          outputEval: $(inputs.output_path + "/." + inputs.output_file)
        doc: "Workspace path to the full output folder"

  - id: main
    class: Workflow

    label: SequenceSimilarityClustering
    doc: "Cluster sequences from a FASTA/FASTQ file using MMseqs2"

    requirements:
      InlineJavascriptRequirement: {}

    inputs:
      input_file:
        type: string
        doc: "Workspace path to the input FASTA or FASTQ file [bvbrc:wstype]"

      identity:
        type: float?
        default: 0.95
        doc: "Minimum sequence identity (0.0-1.0). 0.95=species, 0.90=genus, 0.50=broad families"

      coverage:
        type: float?
        default: 0.8
        doc: "Minimum coverage fraction of the shorter sequence (0.0-1.0)"

      cluster_mode:
        type: int?
        default: 0
        doc: "Clustering algorithm: 0=greedy set cover, 1=connected component, 2=greedy incremental"

      output_path:
        type: string
        doc: "Workspace output folder [bvbrc:folder]"

      output_file:
        type: string
        doc: "Output basename [bvbrc:wsid]"

    outputs:
      clustered_fasta:
        type: string
        outputSource: cluster/clustered_fasta

      cluster_report:
        type: string
        outputSource: cluster/cluster_report

      summary:
        type: string
        outputSource: cluster/summary

      result_folder:
        type: string?
        outputSource: cluster/result_folder

    steps:
      cluster:
        in:
          input_file: input_file
          identity: identity
          coverage: coverage
          cluster_mode: cluster_mode
          output_path: output_path
          output_file: output_file
        out:
          - clustered_fasta
          - cluster_report
          - summary
          - result_folder
        run: "#SSC"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

cwlVersion: v1.2
