cwlVersion: v1.2
class: CommandLineTool
id: lofreq-viterbi
label: LoFreq viterbi - realign reads around indels, then coordinate-sort
doc: |
  Galaxy IWC equivalent: lofreq_viterbi 2.1.5+galaxy0 (no --keepflags,
  --defqual 2), followed by samtools sort as in the Galaxy wrapper.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14
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
      samtools faidx reference.fa
      lofreq viterbi --ref reference.fa --defqual 2 --out unsorted.bam "$2"
      samtools sort --no-PG -T sort.tmp -@ "$3" -O BAM -o "$4" unsorted.bam
      rm -f unsorted.bam
  - position: 1
    valueFrom: lofreq-viterbi
  - position: 5
    valueFrom: $(inputs.sample_name).realigned.bam

inputs:
  reference:
    type: File
    inputBinding:
      position: 2
  bam:
    type: File
    inputBinding:
      position: 3
  threads:
    type: int
    default: 2
    inputBinding:
      position: 4
  sample_name:
    type: string

outputs:
  realigned_bam:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).realigned.bam
