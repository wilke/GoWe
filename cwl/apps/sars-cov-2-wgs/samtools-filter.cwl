cwlVersion: v1.2
class: CommandLineTool
id: samtools-filter
label: samtools - coordinate-sort and keep properly paired reads with MAPQ >= 20
doc: |
  Galaxy IWC equivalent: the coordinate sort done inside bwa_mem, then
  samtools_view 1.22+galaxy2 with -q 20 and inclusive flags 1,2 (-f 3:
  paired and properly paired), BAM output.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/samtools:1.22--h96c455f_0
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
      samtools sort --no-PG -@ "$2" -T sort.tmp -O BAM -o sorted.bam "$1"
      samtools view --no-PG -@ "$2" -b -q "$3" -f 3 -o "$4" sorted.bam
      rm -f sorted.bam
  - position: 1
    valueFrom: samtools-filter
  - position: 5
    valueFrom: $(inputs.sample_name).filtered.bam

inputs:
  alignments:
    type: File
    inputBinding:
      position: 2
  threads:
    type: int
    default: 2
    inputBinding:
      position: 3
  min_mapq:
    type: int
    default: 20
    inputBinding:
      position: 4
  sample_name:
    type: string

outputs:
  bam:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).filtered.bam
