cwlVersion: v1.2
class: CommandLineTool

doc: "Fastq Utilites — Useful common processing of fastq files"

hints:
  goweHint:
    bvbrc_app_id: FastqUtils
    executor: bvbrc

baseCommand: [FastqUtils]

inputs:
  reference_genome_id:
    type: string?
    doc: "Reference genome ID"
  paired_end_libs:
    type:
      - "null"
      - type: record
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
            doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore]"
            default: "infer"
    doc: "Paired-end reads (singular record)"
  single_end_libs:
    type:
      - "null"
      - type: record
        name: single_end_lib
        fields:
          - name: read
            type: File
            doc: "Read file"
          - name: platform
            type: string?
            doc: "Sequencing platform [enum: infer, illumina, pacbio, pacbio_hifi, nanopore]"
            default: "infer"
    doc: "Single-end reads (singular record)"
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
  output_path:
    type: Directory
    doc: "Path to which the output will be written. Defaults to the directory containing the input data.  [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  recipe:
    type: string[]
    doc: "Recipe [bvbrc:list]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
