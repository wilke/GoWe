cwlVersion: v1.2
class: CommandLineTool
id: lofreq-indelqual
label: LoFreq indelqual - add Dindel indel qualities
doc: |
  Galaxy IWC equivalent: lofreq_indelqual 2.1.5+galaxy1, strategy dindel.
  Writes the BAM and its index (lofreq call needs the index).

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14
  ResourceRequirement:
    coresMin: 1
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
      lofreq indelqual --dindel --ref reference.fa -o "$3" "$2"
      samtools index "$3"
  - position: 1
    valueFrom: lofreq-indelqual
  - position: 4
    valueFrom: $(inputs.sample_name).indelqual.bam

inputs:
  reference:
    type: File
    inputBinding:
      position: 2
  bam:
    type: File
    inputBinding:
      position: 3
  sample_name:
    type: string

outputs:
  indelqual_bam:
    type: File
    secondaryFiles:
      - .bai
    outputBinding:
      glob: $(inputs.sample_name).indelqual.bam
