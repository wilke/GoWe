cwlVersion: v1.2
class: CommandLineTool
id: combine-results
label: Combine per-sample consensus genomes and variant tables
doc: |
  Galaxy IWC equivalent: tp_cat over the per-sample consensus FASTAs and
  collapse_dataset 5.1.0 over the per-sample variant tables (one header,
  sample name added as the first column "Sample"). Sample names are taken
  from the file names (<name>.consensus.fa, <name>.variants.tsv).

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/samtools:1.22--h96c455f_0
  ResourceRequirement:
    coresMin: 1
    ramMin: 1000
  InitialWorkDirRequirement:
    listing:
      - entryname: combine.sh
        entry: |
          set -euo pipefail
          n_cons="$1"; shift
          : > consensus_genomes.fa
          for ((i = 0; i < n_cons; i++)); do cat "$1" >> consensus_genomes.fa; shift; done
          first=1
          for t in "$@"; do
            name=`basename "$t" .variants.tsv`
            if [ "$first" = 1 ]; then
              head -n 1 "$t" | sed 's/^/Sample\t/' > variant_report.tsv
              first=0
            fi
            tail -n +2 "$t" | sed "s/^/$name\t/" >> variant_report.tsv
          done

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [bash, combine.sh]

inputs:
  consensus:
    type: File[]
    inputBinding: {position: 2}
  variant_tables:
    type: File[]
    inputBinding: {position: 3}

arguments:
  - {position: 1, valueFrom: $(inputs.consensus.length)}

outputs:
  consensus_genomes:
    type: File
    outputBinding: {glob: consensus_genomes.fa}
  variant_report:
    type: File
    outputBinding: {glob: variant_report.tsv}
