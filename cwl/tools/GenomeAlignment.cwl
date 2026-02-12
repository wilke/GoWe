cwlVersion: v1.2
class: CommandLineTool

doc: "Multiple Whole Genome Alignment — Uses Mauve to perform multiple whole genome alignment with rearrangements."

hints:
  goweHint:
    bvbrc_app_id: GenomeAlignment
    executor: bvbrc

baseCommand: [GenomeAlignment]

inputs:
  genome_ids:
    type: string
    doc: "Genome IDs to Align"
  recipe:
    type: string?
    doc: "Mauve method to be used"
    default: "progressiveMauve"
  seedWeight:
    type: float?
    doc: "Seed weight for calculating initial anchors."
  maxGappedAlignerLength:
    type: float?
    doc: "Maximum number of base pairs to attempt aligning with the gapped aligner."
  maxBreakpointDistanceScale:
    type: float?
    doc: "Set the maximum weight scaling by breakpoint distance.  Must be in [0, 1]. Defaults to 0.9."
  conservationDistanceScale:
    type: float?
    doc: "Scale conservation distances by this amount.  Must be in [0, 1].  Defaults to 1."
  weight:
    type: float?
    doc: "Minimum pairwise LCB score."
  minScaledPenalty:
    type: float?
    doc: "Minimum breakpoint penalty after scaling the penalty by expected divergence."
  hmmPGoHomologous:
    type: float?
    doc: "Probability of transitioning from the unrelated to the homologous state"
  hmmPGoUnrelated:
    type: float?
    doc: "Probability of transitioning from the homologous to the unrelated state"
  output_path:
    type: string
    doc: "Path to which the output will be written. Defaults to the directory containing the input data."
  output_file:
    type: string
    doc: "Basename for the generated output files. Defaults to the basename of the input data."

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
