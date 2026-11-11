cwlVersion: v1.2
class: CommandLineTool
id: qualimap-bamqc
label: QualiMap BamQC - coverage and mapping quality report
doc: |
  Galaxy IWC equivalent: qualimap_bamqc 2.3+galaxy0 with
  --collect-overlap-pairs, -nw 400, --paint-chromosome-limits, -hm 3,
  --skip-duplicated --skip-dup-mode 0. The output directory holds
  genome_results.txt and raw_data_qualimapReport/ for MultiQC, plus the
  HTML report. The IWC workflow tolerates a failed QualiMap run (Filter
  failed datasets); here the step is required.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/qualimap:2.3--hdfd78af_0
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: 4000
  EnvVarRequirement:
    envDef:
      JAVA_OPTS: $("-Xmx" + Math.floor(runtime.ram * 0.8) + "m")

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [bash, -euo, pipefail, -c]

arguments:
  - position: 0
    valueFrom: |
      ln -s "$1" "$3.bam"
      unset DISPLAY
      qualimap bamqc -bam "$3.bam" -outdir "$3" -outformat html \
        --collect-overlap-pairs -nw 400 --paint-chromosome-limits -hm 3 \
        --skip-duplicated --skip-dup-mode 0 -nt "$2" > qualimap.log 2>&1 \
        || { cat qualimap.log >&2; exit 1; }
  - {position: 1, valueFrom: qualimap-bamqc}

inputs:
  bam:
    type: File
    inputBinding: {position: 2}
  threads:
    type: int
    default: 2
    inputBinding: {position: 3}
  sample_name:
    type: string
    inputBinding: {position: 4}

outputs:
  report_dir:
    type: Directory
    outputBinding: {glob: $(inputs.sample_name)}
