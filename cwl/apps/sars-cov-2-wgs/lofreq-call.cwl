cwlVersion: v1.2
class: CommandLineTool
id: lofreq-call
label: LoFreq call - sensitive SNV and indel calling across a wide range of allele frequencies
doc: |
  Galaxy IWC equivalent: lofreq_call 2.1.5+galaxy3 with the IWC settings:
  --call-indels --min-cov 5 --max-depth 1000000 --min-bq 30
  --min-alt-bq 30 --min-mq 20 --max-mq 255 --min-jq 0 --min-alt-jq 0
  --def-alt-jq 0 --sig 0.0005 --bonf dynamic --no-default-filter
  (BAQ/IDAQ and extended BAQ at the lofreq defaults, no orphan reads).
  Strand-bias filtering is left to lofreq-filter.cwl.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: 4000

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
      ln -s "$2" reads.bam
      samtools index reads.bam
      lofreq call-parallel --pp-threads "$3" --verbose \
        --ref reference.fa --out variants.vcf --call-indels \
        --min-cov "$5" --max-depth 1000000 \
        --min-bq 30 --min-alt-bq 30 --min-mq 20 --max-mq 255 \
        --min-jq 0 --min-alt-jq 0 --def-alt-jq 0 \
        --sig "$6" --bonf dynamic --no-default-filter \
        reads.bam 2> lofreq-call.log || { cat lofreq-call.log >&2; exit 1; }
      # lofreq call-parallel can emit gzip despite the .vcf name (CSB5/lofreq#85)
      if gzip -t variants.vcf 2>/dev/null; then
        mv variants.vcf variants.vcf.gz && gzip -d variants.vcf.gz
      fi
      mv variants.vcf "$4"
  - position: 1
    valueFrom: lofreq-call
  - position: 5
    valueFrom: $(inputs.sample_name).lofreq.vcf

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
  min_cov:
    type: int
    default: 5
    inputBinding:
      position: 6
  significance:
    type: float
    default: 0.0005
    inputBinding:
      position: 7

outputs:
  variants:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).lofreq.vcf
