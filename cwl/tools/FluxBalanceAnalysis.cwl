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
    type: string[]?
    doc: "Additional compounds to supplement media in FBA simulation"
  geneko:
    type: string[]?
    doc: "Gene knockouts to simulate in FBA"
  rxnko:
    type: string[]?
    doc: "Reaction knockouts to simulate in FBA"
  objective_fraction:
    type: float?
    doc: "Objective fraction for follow up analysis (e.g. FVA, essentiality prediction)"
    default: 1
  uptake_limit:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: uptake_limit_rec
          fields:
            - name: atom
              type: string
              doc: "Atom type [enum: C, N, O, P, S]"
            - name: maxuptake
              type: float
              doc: "Maximum uptake"
    doc: "Uptake limits"
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  custom_bounds:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: custom_bound
          fields:
            - name: vartype
              type: string
              doc: "Variable type [enum: flux, biomassflux, drainflux]"
            - name: variable
              type: string
              doc: "Variable name"
            - name: upperbound
              type: float
              doc: "Upper bound"
            - name: lowerbound
              type: float
              doc: "Lower bound"
    doc: "Custom bounds"
  objective:
    type:
      - "null"
      - type: array
        items:
          type: record
          name: objective_rec
          fields:
            - name: vartype
              type: string
              doc: "Variable type [enum: flux, biomassflux, drainflux]"
            - name: variable
              type: string
              doc: "Variable name"
            - name: coefficient
              type: float
              doc: "Coefficient"
    doc: "Objective function"
  output_path:
    type: Directory?
    doc: "Workspace folder for results (framework parameter) [bvbrc:folder]"

outputs:
  result:
    type: File[]
    outputBinding:
      glob: $(inputs.output_path.location)/$(inputs.output_file)*
