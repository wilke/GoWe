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
    type: File
    doc: "Model on which to run flux balance analysis [bvbrc:wstype]"
  media:
    type: File?
    doc: "Media formulation for flux balance analysis [bvbrc:wstype]"
  fva:
    type: boolean?
    doc: "Minimize and maximize each reaction to permit classificaton of reaction activity [bvbrc:bool]"
    default: false
  predict_essentiality:
    type: boolean?
    doc: "Simulate the knockout of each gene in the model to evaluate gene essentiality [bvbrc:bool]"
    default: false
  minimizeflux:
    type: boolean?
    doc: "Minimize sum of all fluxes in reported optimal solution [bvbrc:bool]"
    default: false
  findminmedia:
    type: boolean?
    doc: "Predict the minimal media formulation that will support growth of current model [bvbrc:bool]"
    default: false
  allreversible:
    type: boolean?
    doc: "Ignore existing reaction reversibilities and make all reactions reversible [bvbrc:bool]"
    default: false
  thermo_const_type:
    type: string?
    doc: "Type of thermodynamic constraints [enum: None, Simple] [bvbrc:enum]"
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
    doc: " [bvbrc:group]"
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  custom_bounds:
    type: string?
    doc: " [bvbrc:group]"
  objective:
    type: string?
    doc: " [bvbrc:group]"
  output_path:
    type: Directory?
    doc: "Workspace folder for results (framework parameter) [bvbrc:folder]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
