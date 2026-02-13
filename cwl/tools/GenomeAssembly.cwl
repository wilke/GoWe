cwlVersion: v1.2
class: CommandLineTool

doc: "Assemble reads — Assemble reads into a set of contigs"

hints:
  goweHint:
    bvbrc_app_id: GenomeAssembly
    executor: bvbrc

baseCommand: [GenomeAssembly]

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
  reference_assembly:
    type: File?
    doc: "Reference set of assembled DNA contigs [bvbrc:wstype]"
  recipe:
    type: string?
    doc: "Recipe used for assembly [enum: auto, full_spades, fast, miseq, smart, kiki] [bvbrc:enum]"
    default: "auto"
  pipeline:
    type: string?
    doc: "Advanced assembly pipeline arguments that overrides recipe"
  min_contig_len:
    type: int?
    doc: "Filter out short contigs in final assembly"
    default: 300
  min_contig_cov:
    type: float?
    doc: "Filter out contigs with low read depth in final assembly"
    default: 5
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
