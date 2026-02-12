cwlVersion: v1.2
class: CommandLineTool

doc: "Run flux balance analysis — Run flux balance analysis on model."

hints:
  goweHint:
    bvbrc_app_id: FluxBalanceAnalysis
    executor: bvbrc

baseCommand: [FluxBalanceAnalysis]

inputs:
  model:
    type: string
    doc: "Model on which to run flux balance analysis"
  media:
    type: string?
    doc: "Media formulation for flux balance analysis"
  fva:
    type: boolean?
    doc: "Minimize and maximize each reaction to permit classificaton of reaction activity"
    default: false
  predict_essentiality:
    type: boolean?
    doc: "Simulate the knockout of each gene in the model to evaluate gene essentiality"
    default: false
  minimizeflux:
    type: boolean?
    doc: "Minimize sum of all fluxes in reported optimal solution"
    default: false
  findminmedia:
    type: boolean?
    doc: "Predict the minimal media formulation that will support growth of current model"
    default: false
  allreversible:
    type: boolean?
    doc: "Ignore existing reaction reversibilities and make all reactions reversible"
    default: false
  thermo_const_type:
    type: string?
    doc: "Type of thermodynamic constraints"
  media_supplement:
    type: string?
    doc: "Additional compounds to supplement media in FBA simulaton"
  geneko:
    type: string?
    doc: "List of gene knockouts to simulation in FBA."
  rxnko:
    type: string?
    doc: "List of reaction knockouts to simulation in FBA."
  objective_fraction:
    type: float?
    doc: "Objective fraction for follow up analysis (e.g. FVA, essentiality prediction)"
    default: 1
  uptake_limit:
    type: string?
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  custom_bounds:
    type: string?
  objective:
    type: string?
  output_path:
    type: string
    doc: "Workspace path for results"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
