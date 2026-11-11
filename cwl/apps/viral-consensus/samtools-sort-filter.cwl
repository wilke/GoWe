cwlVersion: v1.2
class: CommandLineTool
id: samtools-sort-filter
label: samtools - coordinate-sort, then keep properly paired reads with MAPQ >= 20
doc: |
  Galaxy IWC equivalent: the coordinate sort inside bwa_mem (sorted BAM,
  input to samtools stats), then samtools_view 1.22+galaxy2 with -q 20 and
  -f 3 (paired, properly paired). Both BAMs are outputs.

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
      samtools sort --no-PG -@ "$2" -T sort.tmp -O BAM -o "$4.sorted.bam" "$1"
      samtools view --no-PG -@ "$2" -b -q "$3" -f 3 -o "$4.filtered.bam" "$4.sorted.bam"
  - {position: 1, valueFrom: samtools-sort-filter}

inputs:
  alignments:
    type: File
    inputBinding: {position: 2}
  threads:
    type: int
    default: 2
    inputBinding: {position: 3}
  min_mapq:
    type: int
    default: 20
    inputBinding: {position: 4}
  sample_name:
    type: string
    inputBinding: {position: 5}

outputs:
  sorted_bam:
    type: File
    outputBinding: {glob: $(inputs.sample_name).sorted.bam}
  filtered_bam:
    type: File
    outputBinding: {glob: $(inputs.sample_name).filtered.bam}
