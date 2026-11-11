cwlVersion: v1.2
class: CommandLineTool
id: ivar-consensus
label: iVar consensus - consensus genome from a pileup
doc: |
  Galaxy IWC equivalent: ivar_consensus 1.4.4+galaxy0 with -m 20 (minimum
  depth), -c 0.8 (minimum indel frequency), -n N (Ns below minimum
  depth), -q min_qual and -t min_freq, followed by the IWC header rename:
  the FASTA header becomes the sample name.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/ivar:1.4.4--h077b44d_0
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
      samtools mpileup -A -a -d 0 -Q 0 "$1" 2> mpileup.log \
        | ivar consensus -p consensus -q "$2" -t "$3" -c 0.8 -m 20 -n N
      sed -E "1s/^>.*/>$4/" consensus.fa > "$4.consensus.fa"
  - {position: 1, valueFrom: ivar-consensus}

inputs:
  bam:
    type: File
    inputBinding: {position: 2}
  min_qual:
    type: int
    inputBinding: {position: 3}
  min_freq:
    type: float
    inputBinding: {position: 4}
  sample_name:
    type: string
    inputBinding: {position: 5}

outputs:
  consensus:
    type: File
    outputBinding: {glob: $(inputs.sample_name).consensus.fa}
