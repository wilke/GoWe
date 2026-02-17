#!/usr/bin/env cwl-runner
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [echo, bwa, mem]

inputs:
  threads:
    type: int
    inputBinding:
      position: 1
      prefix: -t

  reference:
    type: File
    inputBinding:
      position: 2

  reads_r1:
    type: File
    inputBinding:
      position: 3

  reads_r2:
    type: File?
    inputBinding:
      position: 4

arguments:
  - position: 0
    prefix: -M
    valueFrom: ""

outputs:
  output:
    type: stdout

stdout: alignment.sam
