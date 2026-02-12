cwlVersion: v1.2
$graph:
  - id: bvbrc-date
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: Date
        executor: bvbrc
    baseCommand: [Date]
    inputs:
      output_path: { type: "string?" }
      output_file: { type: "string?" }
    outputs:
      date_result: { type: File, outputBinding: { glob: "*.txt" } }

  - id: bvbrc-sleep
    class: CommandLineTool
    hints:
      goweHint:
        bvbrc_app_id: Sleep
        executor: bvbrc
    baseCommand: [Sleep]
    inputs:
      sleep_time: { type: int, default: 1 }
      output_path: { type: string }
      output_file: { type: string }
      trigger: { type: "File?" }
    outputs:
      sleep_result: { type: File, outputBinding: { glob: "*.txt" } }

  - id: main
    class: Workflow
    inputs:
      output_path: string
      output_file: string
      sleep_seconds:
        type: int
        default: 1
    steps:
      get_date:
        run: "#bvbrc-date"
        in:
          output_path: output_path
          output_file: output_file
        out: [date_result]
      wait:
        run: "#bvbrc-sleep"
        in:
          sleep_time: sleep_seconds
          output_path: output_path
          output_file: output_file
          trigger: get_date/date_result
        out: [sleep_result]
    outputs:
      date_output:
        type: File
        outputSource: get_date/date_result
      sleep_output:
        type: File
        outputSource: wait/sleep_result
