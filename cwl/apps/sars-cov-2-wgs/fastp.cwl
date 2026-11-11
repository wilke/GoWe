cwlVersion: v1.2
class: CommandLineTool
id: fastp
label: fastp - read QC and trimming of one paired-end sample
doc: |
  Galaxy IWC equivalent: fastp 1.4.0+galaxy0 with the tool defaults
  (adapter trimming on, quality and length filtering on, no deduplication,
  no merging). Writes trimmed reads plus the JSON and HTML reports.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/fastp:1.4.0--h43da1c4_0
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: 2000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [fastp]

arguments:
  - prefix: --thread
    valueFrom: $(inputs.threads)
  - prefix: --report_title
    valueFrom: $(inputs.sample_name)
  - prefix: -o
    valueFrom: $(inputs.sample_name)_R1.trimmed.fastq.gz
  - prefix: -O
    valueFrom: $(inputs.sample_name)_R2.trimmed.fastq.gz
  - prefix: --json
    valueFrom: $(inputs.sample_name).fastp.json
  - prefix: --html
    valueFrom: $(inputs.sample_name).fastp.html

inputs:
  sample_name:
    type: string
  forward:
    type: File
    inputBinding:
      prefix: -i
  reverse:
    type: File
    inputBinding:
      prefix: -I
  threads:
    type: int
    default: 2

outputs:
  trimmed_forward:
    type: File
    outputBinding:
      glob: $(inputs.sample_name)_R1.trimmed.fastq.gz
  trimmed_reverse:
    type: File
    outputBinding:
      glob: $(inputs.sample_name)_R2.trimmed.fastq.gz
  report_json:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).fastp.json
  report_html:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).fastp.html
