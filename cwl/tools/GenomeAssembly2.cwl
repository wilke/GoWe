cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble WGS reads — Assemble reads into a set of contigs"

hints:
  goweHint:
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
    type: string[]?
    doc: "Sequence Read Archive (SRA) Run IDs"
  recipe:
    type: string?
    doc: "Recipe used for assembly [enum: auto, unicycler, flye, meta-flye, canu, spades, meta-spades, plasmid-spades, single-cell, megahit] [bvbrc:enum]"
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
  normalize:
    type: boolean?
    doc: "Normalize reads (BBNorm)"
    default: false
  filtlong:
    type: boolean?
    doc: "Filter long reads"
    default: false
  target_depth:
    type: int?
    doc: "Target depth"
    default: 200
  max_bases:
    type: int?
    doc: "Max bases triggering downsampling"
    default: 10000000000
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
  genome_size:
    type: int?
    doc: "Estimated genome size (for canu)"
    default: 5000000
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
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
