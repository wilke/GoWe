cwlVersion: v1.2
class: CommandLineTool

label: stability-prediction

doc: >-
  Stability Prediction — Predict protein stability changes using
  ThermoMPNN-D. Supports single, additive, and epistatic saturation
  mutagenesis modes on PDB structures.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: StabilityPrediction
    executor: bvbrc

baseCommand: [StabilityPrediction]

inputs:
  mode:
    type: string
    doc: "Saturation mutagenesis mode [enum: single, additive, epistatic] [bvbrc:enum]"

  pdb_id:
    type: string?
    doc: "PDB ID from PDB Bank to download and use"

  pdb:
    type: string?
    doc: "PDB file to use as input [bvbrc:wsid]"

  chains:
    type: string?
    doc: "Chains to analyze (space-separated, e.g. 'A B C'). Empty string defaults to all chains."
    default: ""

  batch_size:
    type: int?
    doc: "Batch size for stability prediction module"
    default: 256

  threshold:
    type: float?
    doc: "Threshold for SSM sweep in kcal/mol. Only mutations below this value are saved. Set high (e.g. 100) to save all."
    default: 100

  distance:
    type: float?
    doc: "Pairwise Ca distance cutoff for double mutant predictions (Angstroms)"
    default: 100

  ss_penalty:
    type: boolean?
    doc: "Add explicit disulfide breakage penalty [bvbrc:bool]"
    default: true

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  stability_results:
    type: File?
    doc: "Stability prediction results (TSV)"
    outputBinding:
      glob: "$(inputs.output_file)_stability.tsv"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
