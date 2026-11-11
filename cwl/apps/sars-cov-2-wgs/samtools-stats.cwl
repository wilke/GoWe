cwlVersion: v1.2
class: CommandLineTool
id: samtools-stats
label: samtools stats - mapping statistics for MultiQC
doc: |
  Galaxy IWC equivalent: samtools_stats 2.0.9 with default options, run on
  the filtered (pre-deduplication) BAM.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/samtools:1.22--h96c455f_0
  ResourceRequirement:
    coresMin: 1
    ramMin: 1000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [samtools, stats]

stdout: $(inputs.sample_name).samtools_stats.txt

inputs:
  bam:
    type: File
    inputBinding:
      position: 1
  sample_name:
    type: string

outputs:
  stats:
    type: stdout
