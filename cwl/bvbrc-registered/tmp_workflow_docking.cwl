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
    doc: "Type of ligand input [enum: smiles_list, ws_file] [bvbrc:enum]. smiles_list requires ligand_smiles_list; ws_file requires ligand_ws_file. If the user has given no ligand at all, ASK before submitting. (A third value, named_library, exists in the website form but is NOT offered here: the BV-BRC app rejects it with 'Unknown ligand library type selected named_library' — see ligand_named_library.)"

  ligand_named_library:
    type: string?
    doc: "NOT CURRENTLY SUPPORTED — do not set this input. Selecting a built-in ligand library is an unfinished BV-BRC feature: the website form offers it (small_db = Exemplar Drug Compounds, approved-drugs = Approved Drug Compounds, experimental_drugs = Experimental Drug Compounds) but the app rejects the submission with 'Unknown ligand library type selected named_library' (DockingCompute.pm), and Docking.js carries the comment 'ligand library is not working just yet'. The input is kept declared so it can be re-enabled by restoring named_library to the ligand_library_type enum once the app supports it. Use ligand_smiles_list or ligand_ws_file instead."

  ligand_smiles_list:
    type:
      - "null"
      - type: array
        items:
          type: array
          items: string
    doc: "Ligands as [id, smiles] PAIRS — an array of two-element arrays, e.g. [[\"id-1\", \"CCO\"], [\"aspirin\", \"CC(=O)Oc1ccccc1C(=O)O\"]]. NOT a flat list of SMILES strings. The website form builds this by splitting each line on whitespace: a line with two columns becomes [id, smiles], and a bare SMILES string gets a generated id (id-1, id-2, ...). Required when ligand_library_type=smiles_list [bvbrc:array]"

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
