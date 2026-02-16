cwlVersion: v1.2
class: CommandLineTool

doc: "Runs a ProbModelSEED job — Runs a ProbModelSEED modeling job"

hints:
  goweHint:
    bvbrc_app_id: RunProbModelSEEDJob
    executor: bvbrc

baseCommand: [RunProbModelSEEDJob]

inputs:
  command:
    type: string
    doc: "ProbModelSEED command to run"
  arguments:
    type: string
    doc: "ProbModelSEED arguments"

outputs: []
