cwlVersion: v1.2
class: CommandLineTool
id: bwa-mem
label: BWA-MEM - map paired reads to a small reference (indexes it first)
doc: |
  Galaxy IWC equivalent: bwa_mem 0.7.19+galaxy1, Illumina mode, no read
  group, reference taken from the history (indexed on the fly). The bwa
  image has no samtools, so this step writes SAM and samtools-filter.cwl
  sorts and filters it. Indexing per task is fine for viral genomes; for a
  large reference, pre-build the index instead.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/bwa:0.7.19--h577a1d6_1
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: 2000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [bash, -euo, pipefail, -c]

arguments:
  - position: 0
    valueFrom: |
      ln -s "$1" reference.fa
      bwa index reference.fa 2> bwa-index.log
      bwa mem -t "$2" -v 1 reference.fa "$3" "$4" > "$5"
  - position: 1
    valueFrom: bwa-mem
  - position: 6
    valueFrom: $(inputs.sample_name).sam

inputs:
  reference:
    type: File
    inputBinding:
      position: 2
  threads:
    type: int
    default: 2
    inputBinding:
      position: 3
  forward:
    type: File
    inputBinding:
      position: 4
  reverse:
    type: File
    inputBinding:
      position: 5
  sample_name:
    type: string

outputs:
  sam:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).sam
