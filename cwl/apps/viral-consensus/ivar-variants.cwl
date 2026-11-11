cwlVersion: v1.2
class: CommandLineTool
id: ivar-variants
label: iVar variants - call variants from a pileup (TSV)
doc: |
  Galaxy IWC equivalent: ivar_variants 1.4.4+galaxy0:
  samtools mpileup -A -d 0 --reference ref -B -Q 0 | ivar variants -q -t.
  ivar-variants-to-vcf.cwl converts the TSV to VCF.

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
      ln -s "$1" ref.fa
      samtools mpileup -A -d 0 --reference ref.fa -B -Q 0 "$2" 2> mpileup.log \
        | ivar variants -p "$5.variants" -q "$3" -t "$4"
  - {position: 1, valueFrom: ivar-variants}

inputs:
  reference:
    type: File
    inputBinding: {position: 2}
  bam:
    type: File
    inputBinding: {position: 3}
  min_qual:
    type: int
    inputBinding: {position: 4}
  min_freq:
    type: float
    inputBinding: {position: 5}
  sample_name:
    type: string
    inputBinding: {position: 6}

outputs:
  variants_tsv:
    type: File
    outputBinding: {glob: $(inputs.sample_name).variants.tsv}
