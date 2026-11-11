cwlVersion: v1.2
class: CommandLineTool
id: snpeff-sars-cov-2
label: SnpEff (covid19 build) - annotate variant effects on NC_045512.2
doc: |
  Galaxy IWC equivalent: snpeff_sars_cov_2 4.5covid19 with
  -formatEff -classic -no-downstream -no-intergenic -no-upstream -no-utr
  -upDownStreamLen 0 -noLog, genome NC_045512.2 (bundled in the image).
  Writes the annotated VCF and the HTML stats page.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/snpeff:4.5covid19--hdfd78af_2
  ResourceRequirement:
    coresMin: 1
    ramMin: 4000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [snpEff]

arguments:
  - position: 0
    valueFrom: $("-Xmx" + Math.floor(runtime.ram * 0.8) + "m")
  - position: 1
    valueFrom: eff
  - position: 2
    valueFrom: -i
  - {position: 3, valueFrom: vcf}
  - {position: 4, valueFrom: -o}
  - {position: 5, valueFrom: vcf}
  - {position: 6, valueFrom: -upDownStreamLen}
  - {position: 7, valueFrom: "0"}
  - {position: 8, valueFrom: -formatEff}
  - {position: 9, valueFrom: -classic}
  - {position: 10, valueFrom: -no-downstream}
  - {position: 11, valueFrom: -no-intergenic}
  - {position: 12, valueFrom: -no-upstream}
  - {position: 13, valueFrom: -no-utr}
  - {position: 14, valueFrom: -stats}
  - {position: 15, valueFrom: $(inputs.sample_name).snpeff.html}
  - {position: 16, valueFrom: -noLog}
  - {position: 17, valueFrom: NC_045512.2}

stdout: $(inputs.sample_name).snpeff.vcf

inputs:
  vcf:
    type: File
    inputBinding:
      position: 18
  sample_name:
    type: string

outputs:
  annotated_vcf:
    type: stdout
  stats_html:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).snpeff.html
  genes_txt:
    type: File?
    outputBinding:
      glob: $(inputs.sample_name).snpeff.genes.txt
