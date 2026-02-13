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
    type: File
    doc: "Model on which to run flux balance analysis [bvbrc:wstype]"
  media:
    type: File?
    doc: "Media formulation for flux balance analysis [bvbrc:wstype]"
  probanno:
    type: File?
    doc: "Computed alternative potential annotations for genes to use in gapfilling functions [bvbrc:wstype]"
  alpha:
    type: float?
    doc: "Increase alpha to increase piority for comprehensive gapfilling"
    default: 0
  allreversible:
    type: boolean?
    doc: "Ignore existing reaction reversibilities and make all reactions reversible [bvbrc:bool]"
    default: false
  allowunbalanced:
    type: boolean?
    doc: "Allow unbalanced reactions in gapfilling [bvbrc:bool]"
    default: false
  integrate_solution:
    type: boolean?
    doc: "Integrate first gapfilling solution [bvbrc:bool]"
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
    doc: "Basename for the generated output files. Defaults to the basename of the input data. [bvbrc:wsid]"
  uptake_limit:
    type: string?
    doc: " [bvbrc:group]"
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
