cwlVersion: v1.2
class: CommandLineTool
label: Boltz structure prediction test

requirements:
  EnvVarRequirement:
    envDef:
      - envName: NUMBA_CACHE_DIR
        envValue: /tmp
      - envName: BOLTZ_CACHE
        envValue: /tmp/boltz_cache

hints:
  DockerRequirement:
    dockerPull: "dxkb/boltz-bvbrc:latest-gpu"

baseCommand: ["boltz", "predict"]

arguments:
  - prefix: "--out_dir"
    valueFrom: $(runtime.outdir)
  - "--use_msa_server"
  - prefix: "--recycling_steps"
    valueFrom: "3"

inputs:
  input_yaml:
    type: File
    inputBinding:
      position: 100

outputs:
  predicted_cif:
    type: File
    outputBinding:
      glob: "boltz_results_*/predictions/*/*_model_0.cif"

  confidence_json:
    type: File
    outputBinding:
      glob: "boltz_results_*/predictions/*/confidence_*_model_0.json"
