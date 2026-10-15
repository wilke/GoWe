cwlVersion: v1.2
class: CommandLineTool
id: kraken2
label: Kraken2 - k-mer taxonomic classification of paired reads
doc: |
  Classifies the reads of one paired-end sample with Kraken2 against a
  pre-built database directory (e.g. a k2_pluspf_16_GB_* build from
  https://benlangmead.github.io/aws-indexes/k2) that lives on the worker host.
  Writes <sample>.kraken2.report.txt (standard Kraken2 report with
  minimizer columns) and <sample>.kraken2.log. Per-read classifications are
  discarded unless `per_read_output` is true (they are as large as the input).

  --memory-mapping is used so concurrent tasks on one host share the
  database through the page cache instead of each loading 16 GB.

  Image: kraken2_latest.sif (Kraken 2.1.2), resolved via the worker's --image-dir.

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: kraken2_latest.sif
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: $(inputs.ram_mb)

hints:
  gowe:Execution:
    executor: worker

baseCommand: [kraken2]

arguments:
  - --paired
  - --memory-mapping
  - --report-minimizer-data
  - prefix: --report
    valueFrom: $(inputs.sample).kraken2.report.txt
  - prefix: --output
    valueFrom: |-
      ${ return inputs.per_read_output ? inputs.sample + ".kraken2.output.txt" : "/dev/null"; }

inputs:
  sample:
    type: string
    doc: Sample name used for output file names.
  reads:
    type: File[]
    doc: R1 and R2 FASTQ files (gzip is detected automatically).
    inputBinding:
      position: 100
  database:
    type: Directory
    doc: Kraken2 database directory (hash.k2d, opts.k2d, taxo.k2d) on the worker host.
    inputBinding:
      prefix: --db
  confidence:
    type: float
    default: 0.0
    doc: Kraken2 --confidence threshold (0-1).
    inputBinding:
      prefix: --confidence
  threads:
    type: int
    default: 8
    inputBinding:
      prefix: --threads
  ram_mb:
    type: int
    default: 24000
    doc: Memory per task (MB); the 16 GB database is memory-mapped.
  per_read_output:
    type: boolean
    default: false

outputs:
  report:
    type: File
    outputBinding:
      glob: $(inputs.sample).kraken2.report.txt
  per_read:
    type: File?
    outputBinding:
      glob: $(inputs.sample).kraken2.output.txt
  log:
    type: File
    outputBinding:
      glob: $(inputs.sample).kraken2.log

stderr: $(inputs.sample).kraken2.log
