cwlVersion: v1.2
class: CommandLineTool
id: snpeff-eff
label: SnpEff eff - annotate variants with the custom viralDB database
doc: |
  Galaxy IWC equivalent: snpEff 5.4+galaxy0 with a custom database
  (-upDownStreamLen 0, -spliceSiteSize 2, stats HTML, -noLog), followed by
  the IWC "Remove annotations for suspicious transcripts" find-and-replace:
  annotations flagged WARNING_TRANSCRIPT_INCOMPLETE are dropped.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/snpeff:5.4.0c--hdfd78af_0
  ResourceRequirement:
    coresMin: 1
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
      snpEff -Xmx"$4"m eff -i vcf -o vcf -upDownStreamLen 0 -spliceSiteSize 2 \
        -s "$3.snpeff.html" -noLog -dataDir "$1" \
        -configOption viralDB.genome=viralDB -configOption viralDB.codonTable=Standard \
        viralDB "$2" > raw.vcf 2> snpeff.log || { cat snpeff.log >&2; exit 1; }
      sed -E 's/[^=,]*WARNING_TRANSCRIPT_INCOMPLETE,?//g; s/\|,$/|/' raw.vcf > "$3.snpeff.vcf"
  - {position: 1, valueFrom: snpeff-eff}
  - {position: 5, valueFrom: $(Math.floor(runtime.ram * 0.8))}

inputs:
  snpeff_db:
    type: Directory
    inputBinding: {position: 2}
  vcf:
    type: File
    inputBinding: {position: 3}
  sample_name:
    type: string
    inputBinding: {position: 4}

outputs:
  annotated_vcf:
    type: File
    outputBinding: {glob: $(inputs.sample_name).snpeff.vcf}
  stats_html:
    type: File
    outputBinding: {glob: $(inputs.sample_name).snpeff.html}
