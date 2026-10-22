# Custom fixture for GoWe's acceptance battery (issue #260), delivery
# scoping (group 3): three steps sharing one CommandLineTool ("dump-env")
# that dumps every GOWE_TEST_TOKEN_* env var it can see, sorted, to stdout.
#   - stepA opts in via gowe:Execution.secret_env: [GOWE_TEST_TOKEN_A] and
#     must see A and only A.
#   - stepB carries no gowe:Execution hint at all and must see neither.
#   - stepC opts in via gowe:Execution.inject_secrets: true and must see
#     both A and B.
# Also the env-var consumption mode fixture for group 5.
cwlVersion: v1.2

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

$graph:
  - id: dump-env
    class: CommandLineTool
    baseCommand: ["sh", "-c", "env | grep '^GOWE_TEST_TOKEN_' | sort || true"]
    inputs: []
    outputs:
      out:
        type: stdout

  - id: main
    class: Workflow
    inputs: []
    outputs:
      a_out:
        type: File
        outputSource: stepA/out
      b_out:
        type: File
        outputSource: stepB/out
      c_out:
        type: File
        outputSource: stepC/out
    steps:
      stepA:
        run: "#dump-env"
        # executor: worker — see testdata/secrets/secret_wf_iwdr.cwl's
        # comment (#260 review H4): the server-side local executor does not
        # deliver RuntimeHints.Secrets at all today, only a real worker does.
        hints:
          gowe:Execution:
            executor: worker
            secret_env: [GOWE_TEST_TOKEN_A]
        in: {}
        out: [out]
      stepB:
        run: "#dump-env"
        hints:
          gowe:Execution:
            executor: worker
        in: {}
        out: [out]
      stepC:
        run: "#dump-env"
        hints:
          gowe:Execution:
            executor: worker
            inject_secrets: true
        in: {}
        out: [out]
