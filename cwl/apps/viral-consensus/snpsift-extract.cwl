cwlVersion: v1.2
class: CommandLineTool
id: snpsift-extract
label: SnpSift extractFields - per-sample variant table
doc: |
  Galaxy IWC equivalent: snpSift_extractFields 5.4.0c+galaxy0 with
  separator "," for multiple values, "." for empty fields and the fields
  CHROM POS REF ALT QUAL DP AF DP4 ANN[*].EFFECT ANN[*].IMPACT
  ANN[*].GENE ANN[*].TRID ANN[*].HGVS_C ANN[*].HGVS_P.

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: quay.io/biocontainers/snpsift:5.4.0c--hdfd78af_0
  ResourceRequirement:
    coresMin: 1
    ramMin: 2000

hints:
  gowe:Execution:
    executor: worker

$namespaces:
  gowe: https://github.com/wilke/GoWe#

baseCommand: [SnpSift]

arguments:
  - {position: 0, valueFrom: $("-Xmx" + Math.floor(runtime.ram * 0.8) + "m")}
  - {position: 1, valueFrom: extractFields}
  - {position: 2, valueFrom: -s}
  - {position: 3, valueFrom: ","}
  - {position: 4, valueFrom: -e}
  - {position: 5, valueFrom: "."}
  - position: 7
    valueFrom: $(["CHROM", "POS", "REF", "ALT", "QUAL", "DP", "AF", "DP4",
      "ANN[*].EFFECT", "ANN[*].IMPACT", "ANN[*].GENE", "ANN[*].TRID",
      "ANN[*].HGVS_C", "ANN[*].HGVS_P"])

stdout: $(inputs.sample_name).variants.tsv

inputs:
  vcf:
    type: File
    inputBinding: {position: 6}
  sample_name: string

outputs:
  table:
    type: stdout
