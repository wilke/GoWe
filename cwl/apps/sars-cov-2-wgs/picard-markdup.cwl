cwlVersion: v1.2
class: CommandLineTool
id: picard-markdup
label: Picard MarkDuplicates - remove PCR duplicates
doc: |
  Galaxy IWC equivalent: picard_MarkDuplicates 3.1.1.0 with
  REMOVE_DUPLICATES=true, ASSUME_SORTED=true,
  DUPLICATE_SCORING_STRATEGY=SUM_OF_BASE_QUALITIES,
  OPTICAL_DUPLICATE_PIXEL_DISTANCE=100, VALIDATION_STRINGENCY=LENIENT.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/picard:3.1.1--hdfd78af_1
  ResourceRequirement:
    coresMin: 1
    ramMin: 4000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [picard]

arguments:
  - position: 0
    valueFrom: $("-Xmx" + Math.floor(runtime.ram * 0.8) + "m")
  - position: 1
    valueFrom: MarkDuplicates
  - position: 3
    valueFrom: $("OUTPUT=" + inputs.sample_name + ".dedup.bam")
  - position: 3
    valueFrom: $("METRICS_FILE=" + inputs.sample_name + ".markdup_metrics.txt")
  - position: 3
    valueFrom: REMOVE_DUPLICATES=true
  - position: 3
    valueFrom: ASSUME_SORTED=true
  - position: 3
    valueFrom: DUPLICATE_SCORING_STRATEGY=SUM_OF_BASE_QUALITIES
  - position: 3
    valueFrom: OPTICAL_DUPLICATE_PIXEL_DISTANCE=100
  - position: 3
    valueFrom: VALIDATION_STRINGENCY=LENIENT
  - position: 3
    valueFrom: QUIET=true
  - position: 3
    valueFrom: VERBOSITY=ERROR
  - position: 3
    valueFrom: $("TMP_DIR=" + runtime.tmpdir)

inputs:
  bam:
    type: File
    inputBinding:
      position: 2
      prefix: INPUT=
      separate: false
  sample_name:
    type: string

outputs:
  dedup_bam:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).dedup.bam
  metrics:
    type: File
    outputBinding:
      glob: $(inputs.sample_name).markdup_metrics.txt
