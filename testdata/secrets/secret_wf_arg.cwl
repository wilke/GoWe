# Custom fixture for GoWe's acceptance battery (issue #260), companion to
# secret_wf_iwdr.cwl: exercises the "$(inputs.x) in a command-line argument"
# consumption mode, which the ported cwltool pair does not cover (cwltool's
# own secret_job.cwl only ever puts the secret into a staged file via
# InitialWorkDirRequirement). Otherwise identical in shape/namespacing to
# secret_wf_iwdr.cwl so both flow through the same
# register -> submit -> dispatch -> local-execute -> verify pipeline.
#
# Also doubles as the group-6 (logging) fixture: the secret value lands
# directly in the built command line (cmdResult.Command) as well as in
# captured stdout, so a single run proves both
#   - the value reaches the tool/output correctly (group 5), and
#   - it never appears unmasked in the "built command"/"executing locally"
#     debug logs, and is redacted to ***REDACTED*** in the task's captured
#     stdout (group 6) while the real value still lands in the actual output
#     *file* on disk (which downstream steps need).
cwlVersion: v1.2

$namespaces:
  cwltool: "http://commonwl.org/cwltool#"
  gowe: "https://github.com/wilke/GoWe#"

$graph:
  - id: secret-arg-job
    class: CommandLineTool
    baseCommand: [echo]
    inputs:
      pw:
        type: string
        inputBinding: {}
    outputs:
      out:
        type: stdout

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
        run: "#secret-arg-job"
        # executor: worker — see secret_wf_iwdr.cwl's comment (#260 review H4).
        hints:
          gowe:Execution:
            executor: worker
        in:
          pw: pw
        out: [out]
