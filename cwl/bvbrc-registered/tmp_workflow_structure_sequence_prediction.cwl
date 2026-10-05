cwlVersion: v1.2
class: CommandLineTool

label: protein-sequence-design-proteinmpnn

doc: >-
  Protein Sequence Design (ProteinMPNN) — given an EXISTING protein structure (a PDB
  file or PDB ID), design amino-acid sequences that would fold into it. This is
  structure → sequence. It does NOT predict a structure from a sequence, and it does
  not accept DNA, protein FASTA, or SMILES as input.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: StructureSequencePrediction
    executor: bvbrc

baseCommand: [StructureSequencePrediction]

inputs:
  pdb:
    type: string?
    doc: "Workspace path to a single PDB file to be designed [bvbrc:wsid]"

  pdb_id:
    type: string?
    doc: "PDB ID from PDB Bank"

  pdb_path_chains:
    type: string?
    doc: "Chains to design for the PDB (e.g. 'AB')"

  ca_only:
    type: boolean?
    doc: "Parse CA-only structures and use CA-only models"
    default: false

  model_name:
    type: string
    doc: "ProteinMPNN model name [enum: v_48_002, v_48_010, v_48_020, v_48_030] [bvbrc:enum]"
    default: "v_48_020"

  use_soluble_model:
    type: boolean?
    doc: "Load ProteinMPNN weights trained on soluble proteins only"
    default: false

  backbone_noise:
    type: float?
    doc: "Standard deviation of Gaussian noise to add to backbone atoms"
    default: 0.00

  num_seq_per_target:
    type: int?
    doc: "Number of sequences to generate per target"
    default: 1

  batch_size:
    type: int?
    doc: "Batch size (reduce if running out of GPU memory)"
    default: 1

  sampling_temp:
    type: float?
    doc: "Sampling temperature for amino acids (higher values increase diversity)"
    default: 0.1

  omit_AAs:
    type: string?
    doc: "Amino acids to omit from generated sequences (e.g. 'AC' omits alanine and cysteine)"
    default: "X"

  pssm_multi:
    type: float?
    doc: "PSSM weight between 0.0 (ignore PSSM) and 1.0 (ignore MPNN predictions)"
    default: 0.0

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  designed_sequences:
    type: File?
    doc: "Designed sequences (FASTA)"
    outputBinding:
      glob: "$(inputs.output_file).fasta"

  score_file:
    type: File?
    doc: "ProteinMPNN scoring output (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_scores.json"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
