cwlVersion: v1.2
class: CommandLineTool
id: multiqc
label: MultiQC - combined preprocessing and mapping report
doc: |
  Galaxy IWC equivalent: multiqc 1.35+galaxy4 over fastp JSON,
  samtools stats and Picard MarkDuplicates metrics of all samples.

requirements:
  InlineJavascriptRequirement: {}
  InitialWorkDirRequirement:
    listing: $(inputs.reports)
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

baseCommand: [multiqc, --force, --no-ansi]

arguments:
  - prefix: --filename
    valueFrom: $(inputs.report_name).html
  - prefix: --outdir
    valueFrom: .
  - valueFrom: .
    position: 10

inputs:
  reports:
    type: File[]
  report_name:
    type: string
    default: multiqc_report

outputs:
  report_html:
    type: File
    outputBinding:
      glob: $(inputs.report_name).html
  report_data:
    type: Directory?
    outputBinding:
      glob: $(inputs.report_name)_data
