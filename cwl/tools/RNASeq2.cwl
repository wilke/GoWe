cwlVersion: v1.2
class: CommandLineTool

doc: "Analyze RNASeq reads — Align or assemble RNASeq reads into transcripts with normalized expression levels"

hints:
  goweHint:
    bvbrc_app_id: RNASeq2
    executor: bvbrc

baseCommand: [RNASeq2]

inputs:
  experimental_conditions:
    type: string?
    doc: "Experimental conditions"
  contrasts:
    type: string?
    doc: "Contrast list"
  strand_specific:
    type: boolean?
    doc: "Are the reads in this study strand-specific? [bvbrc:bool]"
    default: true
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
  srr_libs:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: srr_lib
          fields:
            - name: srr_accession
              type: string
              doc: "SRA run accession"
    doc: " [bvbrc:group]"
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  genome_type:
    type: string?
    doc: "genome is type bacteria or host [enum: bacteria, host] [bvbrc:enum]"
  recipe:
    type: string?
    doc: "Recipe used for RNAseq analysis [enum: RNA-Rocket, Rockhopper, Host] [bvbrc:enum]"
    default: "RNA-Rocket"
  host_ftp:
    type: string?
    doc: "Host FTP prefix for obtaining files"
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data. [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  unit_test:
    type: string?
    doc: "Path to the json file used for unit testing"
  skip_sampling:
    type: string?
    doc: "flag to skip the sampling step in alignment.py"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
