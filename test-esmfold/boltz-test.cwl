cwlVersion: v1.2
class: CommandLineTool
label: Boltz structure prediction test

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  EnvVarRequirement:
    envDef:
      - envName: NUMBA_CACHE_DIR
        envValue: /tmp
      - envName: BOLTZ_CACHE
        envValue: /tmp/boltz_cache

hints:
  DockerRequirement:
    dockerPull: "boltz.sif"
  gowe:ResourceData:
    datasets:
      - id: boltz
        path: /local_databases/boltz
        size: 50GB
        mode: cache

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
