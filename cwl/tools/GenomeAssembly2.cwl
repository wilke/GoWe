cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble WGS reads — Assemble reads into a set of contigs"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: GenomeAssembly2
    executor: bvbrc

baseCommand: [GenomeAssembly2]

inputs:
  paired_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: paired_end_lib
          fields:
            - name: read1
              type: File
              doc: "Forward reads"
            - name: read2
              type: File?
              doc: "Reverse reads"
            - name: platform
              type: string?
              doc: "Sequencing platform"
              default: "infer"
            - name: interleaved
              type: boolean
              default: false
            - name: read_orientation_outward
              type: boolean
              default: false
    doc: " [bvbrc:group]"
  single_end_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: single_end_lib
          fields:
            - name: read
              type: File
              doc: "Read file"
            - name: platform
              type: string?
              doc: "Sequencing platform"
              default: "infer"
    doc: " [bvbrc:group]"
  srr_ids:
    type: string?
    doc: "Sequence Read Archive (SRA) Run ID"
  recipe:
    type: string?
    doc: "Recipe used for assembly [enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell] [bvbrc:enum]"
    default: "auto"
  racon_iter:
    type: int?
    doc: "Racon polishing iterations (for long reads)"
    default: 2
  pilon_iter:
    type: int?
    doc: "Pilon polishing iterations (for short reads)"
    default: 2
  trim:
    type: boolean?
    doc: "Trim reads before assembly"
    default: false
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
  genome_size:
    type: string?
    doc: "Estimated genome size (for canu)"
    default: "5M"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  debug_level:
    type: int?
    doc: "Debugging level."
    default: 0

outputs:
  contigs:
    type: File
    doc: "Assembled contigs (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file)_contigs.fasta"
  report:
    type: File
    doc: "Assembly quality report (HTML)"
    outputBinding:
      glob: "$(inputs.output_file)_AssemblyReport.html"
  run_details:
    type: File?
    doc: "Assembly run parameters and quality metrics (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_run_details.json"
  quast_report:
    type: File?
    doc: "QUAST assembly quality assessment"
    outputBinding:
      glob: "$(inputs.output_file)_quast_report.txt"
  assembly_graph:
    type: File?
    doc: "Assembly graph (GFA format)"
    outputBinding:
      glob: "$(inputs.output_file)_assembly_graph.gfa"
  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
