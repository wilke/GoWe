cwlVersion: v1.2
class: CommandLineTool
id: snpeff-build
label: SnpEff build - custom annotation database from reference FASTA + GTF
doc: |
  Galaxy IWC equivalent: snpEff_build_gb 5.4+galaxy0 with GTF input
  (-gtf22), genome name viralDB, codon table Standard, -noCheckCds
  -noCheckProtein. Output is a SnpEff data directory (viralDB/*.bin plus
  snpEff.config lines) used by snpeff-eff.cwl. Runs once per workflow.

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
      mkdir -p build/viralDB snpeff_db/viralDB
      ln -s "$1" build/viralDB/sequences.fa
      ln -s "$2" build/viralDB/genes.gtf
      snpEff -Xmx"$3"m build -noCheckCds -noCheckProtein -v \
        -configOption viralDB.genome=viralDB -configOption viralDB.codonTable=Standard \
        -gtf22 -dataDir "$PWD/build" viralDB 2> snpeff-build.log || { cat snpeff-build.log >&2; exit 1; }
      mv build/viralDB/*.bin snpeff_db/viralDB/
      printf 'viralDB.genome : viralDB\nviralDB.codonTable : Standard\n' > snpeff_db/snpEff.config
  - {position: 1, valueFrom: snpeff-build}
  - {position: 4, valueFrom: $(Math.floor(runtime.ram * 0.8))}

inputs:
  reference:
    type: File
    inputBinding: {position: 2}
  annotation_gtf:
    type: File
    inputBinding: {position: 3}

outputs:
  snpeff_db:
    type: Directory
    outputBinding: {glob: snpeff_db}
