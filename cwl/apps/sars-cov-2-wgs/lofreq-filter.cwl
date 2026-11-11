cwlVersion: v1.2
class: CommandLineTool
id: lofreq-filter
label: LoFreq filter - flag (not drop) strand-biased variants
doc: |
  Galaxy IWC equivalent: lofreq_filter 2.1.5+galaxy0 with --no-defaults,
  --print-all (failing records stay, FILTER set to e.g. sb_fdr), no
  coverage, AF or quality filters, strand bias by multiple testing
  correction (FDR, alpha 0.001, compound test, indels excluded).

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14
  ResourceRequirement:
    coresMin: 1
    ramMin: 1000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [lofreq, filter, --no-defaults, --verbose, --print-all,
  -v, "0", -V, "0", -a, "0.0", -A, "0.0", -b, fdr]

arguments:
  - prefix: -o
    valueFrom: $(inputs.sample_name).lofreq.filtered.vcf

inputs:
  vcf:
    type: File
    inputBinding:
      prefix: -i
  sb_alpha:
    type: float
    default: 0.001
    inputBinding:
      prefix: -c
  sample_name:
    type: string

outputs:
  filtered_vcf:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).lofreq.filtered.vcf
