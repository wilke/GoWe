cwlVersion: v1.2
class: CommandLineTool

hints:
  goweHint:
    bvbrc_app_id: GenomeAnnotation
    executor: bvbrc

baseCommand: ["true"]

inputs:
  contigs:
    type: File
    doc: "Assembled contigs"
  scientific_name:
    type: string
    doc: "Scientific name of the organism"
  taxonomy_id:
    type: int
    doc: "NCBI taxonomy ID"

outputs:
  annotated_genome:
    type: File
    outputBinding:
      glob: "*.genome"
