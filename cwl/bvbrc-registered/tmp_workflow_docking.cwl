cwlVersion: v1.2
class: CommandLineTool

label: small-molecule-docking

doc: >-
  Small Molecule Docking — Dock small molecules into protein structures
  using DiffDock.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

hints:
  gowe:Execution:
    bvbrc_app_id: Docking
    executor: bvbrc

baseCommand: [Docking]

inputs:
  protein_input_type:
    type: string
    doc: "Type of protein input [enum: input_pdb, user_pdb_file] [bvbrc:enum]. input_pdb requires input_pdb (PDB IDs); user_pdb_file requires user_pdb_file (workspace PDB files)."

  input_pdb:
    type: string[]?
    doc: "PDB identifiers for input proteins from precomputed structures. Required when protein_input_type=input_pdb [bvbrc:array]"

  user_pdb_file:
    type: string[]?
    doc: "Workspace files containing user-provided PDB files. Required when protein_input_type=user_pdb_file [bvbrc:wsid]"

  ligand_library_type:
    type: string
    doc: "Type of ligand input [enum: ws_file, named_library, smiles_list] [bvbrc:enum]. ws_file requires ligand_ws_file; named_library requires ligand_named_library; smiles_list requires ligand_smiles_list. If the user has given no ligand at all, ASK before submitting."

  ligand_named_library:
    type: string?
    doc: "Name of a built-in ligand library. Required when ligand_library_type=named_library"

  ligand_smiles_list:
    type: string[]?
    doc: "SMILES strings. Required when ligand_library_type=smiles_list [bvbrc:array]"

  ligand_ws_file:
    type: string?
    doc: "Workspace file of SMILES strings. Required when ligand_library_type=ws_file [bvbrc:wsid]"

  top_n:
    type: int?
    doc: "Return the top N results"

  batch_size:
    type: int?
    doc: "DiffDock batch size (adjusted automatically by protein size)"
    default: 10

  output_path:
    type: string
    doc: "Path to which the output will be written [bvbrc:folder]"

  output_file:
    type: string
    doc: "Basename for the generated output files [bvbrc:wsid]"

outputs:
  docking_results:
    type: File?
    doc: "Docking results summary (JSON)"
    outputBinding:
      glob: "$(inputs.output_file)_docking_results.json"

  best_poses:
    type: File?
    doc: "Best docking poses (SDF)"
    outputBinding:
      glob: "$(inputs.output_file)_best_poses.sdf"

  result_folder:
    type: Directory
    doc: "Full output folder"
    outputBinding:
      glob: "."
