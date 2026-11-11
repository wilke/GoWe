cwlVersion: v1.2
class: CommandLineTool
id: multiqc
label: MultiQC - quality control report for all samples
doc: |
  Galaxy IWC equivalent: multiqc 1.35+galaxy4 over fastp JSON, samtools
  stats and QualiMap BamQC results of all samples.

requirements:
  InlineJavascriptRequirement: {}
  InitialWorkDirRequirement:
    listing: $(inputs.reports.concat(inputs.report_dirs))
  DockerRequirement:
    dockerPull: quay.io/biocontainers/multiqc:1.35--pyhdfd78af_2
  ResourceRequirement:
    coresMin: 1
    ramMin: 2000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [multiqc, --force, --no-ansi, --filename, quality_control_report.html, --outdir, ., .]

inputs:
  reports:
    type: File[]
  report_dirs:
    type: Directory[]
    default: []

outputs:
  report_html:
    type: File
    outputBinding: {glob: quality_control_report.html}
