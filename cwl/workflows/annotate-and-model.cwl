cwlVersion: v1.2
$graph:
  - id: bvbrc-annotation
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: GenomeAnnotation
        executor: bvbrc
    baseCommand: [GenomeAnnotation]
    inputs:
      contigs: { type: string }
      scientific_name: { type: string }
      taxonomy_id: { type: int }
      code: { type: int, default: 11 }
      domain: { type: string, default: "Bacteria" }
      output_path: { type: string }
      output_file: { type: string }
    outputs:
      annotated_genome: { type: File, outputBinding: { glob: "*.genome" } }

  - id: bvbrc-model
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: ModelReconstruction
        executor: bvbrc
    baseCommand: [ModelReconstruction]
    inputs:
      genome: { type: File }
      output_path: { type: string }
      output_file: { type: string }
    outputs:
      model: { type: File, outputBinding: { glob: "*.model" } }

  - id: main
    class: Workflow
    inputs:
      contigs: string
      scientific_name: string
      taxonomy_id: int
      output_path: string
      output_file: string
    steps:
      annotate:
        run: "#bvbrc-annotation"
        in:
          contigs: contigs
          scientific_name: scientific_name
          taxonomy_id: taxonomy_id
          output_path: output_path
          output_file: output_file
        out: [annotated_genome]
      reconstruct_model:
        run: "#bvbrc-model"
        in:
          genome: annotate/annotated_genome
          output_path: output_path
          output_file: output_file
        out: [model]
    outputs:
      annotated_genome:
        type: File
        outputSource: annotate/annotated_genome
      metabolic_model:
        type: File
        outputSource: reconstruct_model/model
