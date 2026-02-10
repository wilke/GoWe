cwlVersion: v1.2
class: Workflow

inputs:
  reads_r1: File
  reads_r2: File
  scientific_name: string
  taxonomy_id: int

steps:
  assemble:
    run: tools/bvbrc-assembly.cwl
    in:
      read1: reads_r1
      read2: reads_r2
    out: [contigs]

  annotate:
    run: tools/bvbrc-annotation.cwl
    in:
      contigs: assemble/contigs
      scientific_name: scientific_name
      taxonomy_id: taxonomy_id
    out: [annotated_genome]

outputs:
  genome:
    type: File
    outputSource: annotate/annotated_genome
