cwlVersion: v1.2
class: CommandLineTool
id: ivar-trim
label: iVar trim - remove primer sequences (amplicon data only)
doc: |
  Galaxy IWC equivalent: ivar_trim 1.4.4+galaxy1 with -f amplicon_info
  (reads outside amplicon boundaries discarded), -x 0, -e, -m 5, -q 20,
  -s 4, output piped into samtools sort.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/ivar:1.4.4--h077b44d_0
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
      ivar trim -i "$1" -b "$2" -f "$3" -x 0 -e -m 5 -q 20 -s 4 \
        | samtools sort -@ "$4" -T sort.tmp -o "$5.trimmed.bam" -
  - {position: 1, valueFrom: ivar-trim}

inputs:
  bam:
    type: File
    secondaryFiles: [.bai]
    inputBinding: {position: 2}
  ivar_bed:
    type: File?
    inputBinding: {position: 3}
  amplicon_info:
    type: File?
    inputBinding: {position: 4}
  threads:
    type: int
    default: 2
    inputBinding: {position: 5}
  sample_name:
    type: string
    inputBinding: {position: 6}

outputs:
  trimmed_bam:
    type: File
    outputBinding: {glob: $(inputs.sample_name).trimmed.bam}
