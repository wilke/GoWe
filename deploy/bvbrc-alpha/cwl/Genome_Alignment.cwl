cwlVersion: v1.2
class: CommandLineTool

label: genome-alignment

doc: >-
  Multiple Whole Genome Alignment — Uses Mauve to perform multiple whole
  genome alignment with rearrangements.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: GenomeAlignment
    executor: bvbrc

baseCommand: [GenomeAlignment]

inputs:
  genome_ids:
    type: string[]
    doc: "Genome IDs to align [bvbrc:array]"

  recipe:
    type: string?
    doc: "Mauve method to be used [enum: progressiveMauve, mauveAligner] [bvbrc:enum]"
    default: "progressiveMauve"

  seedWeight:
    type: float?
    doc: "Seed weight for calculating initial anchors"

  maxGappedAlignerLength:
    type: float?
    doc: "Maximum number of base pairs to attempt aligning with the gapped aligner"

  maxBreakpointDistanceScale:
    type: float?
    doc: "Maximum weight scaling by breakpoint distance. Must be in [0, 1]. Defaults to 0.9"

  conservationDistanceScale:
    type: float?
    doc: "Scale conservation distances by this amount. Must be in [0, 1]. Defaults to 1"

  weight:
    type: float?
    doc: "Minimum pairwise LCB score"

  minScaledPenalty:
    type: float?
    doc: "Minimum breakpoint penalty after scaling the penalty by expected divergence"

  hmmPGoHomologous:
    type: float?
    doc: "Probability of transitioning from the unrelated to the homologous state"

  hmmPGoUnrelated:
    type: float?
    doc: "Probability of transitioning from the homologous to the unrelated state"

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  alignment_xmfa:
    type: File?
    doc: "Mauve XMFA alignment output"
    outputBinding:
      glob: "$(inputs.output_file).xmfa"

  backbone:
    type: File?
    doc: "Mauve backbone file"
    outputBinding:
      glob: "$(inputs.output_file).xmfa.backbone"

  guide_tree:
    type: File?
    doc: "Guide tree used by Mauve (Newick)"
    outputBinding:
      glob: "$(inputs.output_file).guide_tree"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
