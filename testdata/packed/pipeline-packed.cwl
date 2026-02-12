cwlVersion: v1.2
$graph:
  - id: bvbrc-assembly
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAssembly2
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      read1: { type: File }
      read2: { type: File }
      recipe: { type: string, default: "auto" }
    outputs:
      contigs: { type: File, outputBinding: { glob: "*.contigs.fasta" } }

  - id: bvbrc-annotation
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAnnotation
        executor: bvbrc
    baseCommand: ["true"]
    inputs:
      contigs: { type: File }
      scientific_name: { type: string }
      taxonomy_id: { type: int }
    outputs:
      annotated_genome: { type: File, outputBinding: { glob: "*.genome" } }

  - id: main
    class: Workflow
    inputs:
      reads_r1: File
      reads_r2: File
      scientific_name: string
      taxonomy_id: int
    steps:
      assemble:
        run: "#bvbrc-assembly"
        in:
          read1: reads_r1
          read2: reads_r2
        out: [contigs]
      annotate:
        run: "#bvbrc-annotation"
        in:
          contigs: assemble/contigs
          scientific_name: scientific_name
          taxonomy_id: taxonomy_id
        out: [annotated_genome]
    outputs:
      genome:
        type: File
        outputSource: annotate/annotated_genome
