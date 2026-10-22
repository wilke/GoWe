# Ported from cwltool's tests/wf/secret_wf.cwl + tests/wf/secret_job.cwl
# (common-workflow-language/cwltool, Apache-2.0, MIT-compatible), fetched
# 2026-09-15 from
#   https://raw.githubusercontent.com/common-workflow-language/cwltool/main/tests/wf/secret_wf.cwl
#   https://raw.githubusercontent.com/common-workflow-language/cwltool/main/tests/wf/secret_job.cwl
#
# Adapted for GoWe's acceptance battery (issue #260):
#   - cwlVersion bumped v1.0 -> v1.2 (GoWe targets v1.2).
#   - Packed into a single $graph document (id "secret-job" for the tool,
#     "main" for the workflow) instead of the original's two-file
#     workflow+`run: secret_job.cwl` layout, matching how GoWe registers
#     workflows (see testdata/packed/pipeline-packed.cwl).
#   - DockerRequirement dropped: the original pins dockerPull:
#     docker.io/debian:stable-slim purely to get `cat`, which is unrelated to
#     what this fixture exists to prove (cwltool:Secrets + IWDR interpolation
#     re-injection); dropping it lets the acceptance battery run this via the
#     local (non-container) executor with no image pull.
#   - `pw` renamed nowhere; kept identical to the original so the fixture is
#     recognizable as the same test. cwltool:Secrets stays on the Workflow
#     only (not duplicated onto the tool): GoWe's parser reads the
#     cwltool:Secrets declaration from the top-level Workflow document only
#     (internal/parser/parser.go's extractSecretInputs / wf.SecretInputs),
#     unlike cwltool itself which honors the hint per-document.
#
# Exercises: cwltool:Secrets stripping at submission, GoWe's
# INPUT_<NAME>-keyed re-injection (pkg/model.SecretNameForInput), and the
# InitialWorkDirRequirement `$(inputs.pw)` interpolation consumption mode
# (issue #260 acceptance battery, groups 5 and 7).
cwlVersion: v1.2

$namespaces:
  cwltool: "http://commonwl.org/cwltool#"
  gowe: "https://github.com/wilke/GoWe#"

$graph:
  - id: secret-job
    class: CommandLineTool
    requirements:
      InitialWorkDirRequirement:
        listing:
          - entryname: example.conf
            entry: |
              username: user
              password: $(inputs.pw)
    inputs:
      pw: string
    outputs:
      out:
        type: stdout
    arguments: [cat, example.conf]

  - id: main
    class: Workflow
    hints:
      "cwltool:Secrets":
        secrets: [pw]
    inputs:
      pw: string
    outputs:
      out:
        type: File
        outputSource: step1/out
    steps:
      step1:
        run: "#secret-job"
        # executor: worker — #260 review finding H4: the server-side local
        # (non-worker) executor does not deliver task.RuntimeHints.Secrets
        # or re-inject cwltool:Secrets inputs at all today, so this fixture
        # is routed through a real gowe-worker (internal/worker), the only
        # path that currently implements delivery correctly.
        hints:
          gowe:Execution:
            executor: worker
        in:
          pw: pw
        out: [out]
