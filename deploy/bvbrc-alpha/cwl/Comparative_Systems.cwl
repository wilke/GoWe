cwlVersion: v1.2
class: CommandLineTool

label: comparative_systems

doc: "Comparative Systems — Compare protein families, pathways, and subsystems across user-selected genomes"

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: ComparativeSystems
    executor: bvbrc

baseCommand: [ComparativeSystems]

inputs:
  genome_ids:
    type: string[]?
    doc: "BV-BRC genome IDs to compare (e.g. [\"83332.12\", \"511145.12\"]) [bvbrc:array]"

  genome_groups:
    type: string[]?
    doc: "Genome group workspace paths (e.g. [\"/user@bvbrc/home/Genome Groups/MyGroup\"]) [bvbrc:array]"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  pathways_table:
    type: File?
    doc: "Pathways comparison table (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_pathways_tables.json"

  subsystems_table:
    type: File?
    doc: "Subsystems comparison table (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_subsystems_tables.json"

  proteinfams_table:
    type: File?
    doc: "Protein families comparison table (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_proteinfams_tables.json"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
