$graph:
    - baseCommand:
        - bash
        - -c
      class: CommandLineTool
      id: tool
      inputs:
        message:
            inputBinding:
                position: 1
            type: string
      label: echo-dev
      outputs:
        out:
            type: stdout
      stdout: output.txt
    - class: Workflow
      id: main
      inputs:
        message:
            type: string
      outputs:
        out:
            outputSource: run_tool/out
            type: stdout
      steps:
        run_tool:
            in:
                message: message
            out:
                - out
            run: '#tool'
cwlVersion: v1.2
