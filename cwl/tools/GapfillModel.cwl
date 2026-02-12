cwlVersion: v1.2
class: CommandLineTool

doc: "Gapfill metabolic model — Run gapfilling on model."

hints:
  goweHint:
    bvbrc_app_id: GapfillModel
    executor: bvbrc

baseCommand: [GapfillModel]

inputs:
  model:
    type: string
    doc: "Model on which to run flux balance analysis"
  media:
    type: string?
    doc: "Media formulation for flux balance analysis"
  probanno:
    type: string?
    doc: "Computed alternative potential annotations for genes to use in gapfilling functions"
  alpha:
    type: float?
    doc: "Increase alpha to increase piority for comprehensive gapfilling"
    default: 0
  allreversible:
    type: boolean?
    doc: "Ignore existing reaction reversibilities and make all reactions reversible"
    default: false
  allowunbalanced:
    type: boolean?
    doc: "Allow unbalanced reactions in gapfilling"
    default: false
  integrate_solution:
    type: boolean?
    doc: "Integrate first gapfilling solution"
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
  target_reactions:
    type: string?
    doc: "List of reactions that should be targets for gapfilling"
  objective_fraction:
    type: float?
    doc: "Objective fraction for follow up analysis (e.g. FVA, essentiality prediction)"
    default: 0.001
  low_expression_theshold:
    type: float?
    doc: "Threshold of expression for gene to be consider inactive"
    default: 1
  high_expression_theshold:
    type: float?
    doc: "Threshold of expression for gene to be consider active"
    default: 1
  output_file:
    type: string?
    doc: "Basename for the generated output files. Defaults to the basename of the input data."
  uptake_limit:
    type: string?
  custom_bounds:
    type: string?
  objective:
    type: string?
  output_path:
    type: Directory?
    doc: "Workspace folder for results (framework parameter)"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
