cwlVersion: v1.2
class: CommandLineTool
id: primer-scheme-prep
label: viramp-hub scheme-convert - iVar primer BED and amplicon info from a primer scheme
doc: |
  The preparation part of the Galaxy ivar_trim 1.4.4+galaxy1 wrapper with
  "compute amplicon info from the primer scheme": converts the scheme to an
  iVar BED and derives the outer-primer amplicon info file. Runs once per
  workflow, not per sample.

requirements:
  DockerRequirement:
    dockerPull: quay.io/biocontainers/viramp-hub:0.1.0--pyhdfd78af_0
  ResourceRequirement:
    coresMin: 1
    ramMin: 1000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [bash, -euo, pipefail, -c]

arguments:
  - position: 0
    valueFrom: |
      ln -s "$1" bed.bed
      scheme-convert --to bed --bed-type ivar -o ivar.bed bed.bed
      scheme-convert --to amplicon-info -r outer -o amplicon_info.tsv ivar.bed
  - {position: 1, valueFrom: primer-scheme-prep}

inputs:
  primer_scheme:
    type: File
    inputBinding: {position: 2}

outputs:
  ivar_bed:
    type: File
    outputBinding: {glob: ivar.bed}
  amplicon_info:
    type: File
    outputBinding: {glob: amplicon_info.tsv}
