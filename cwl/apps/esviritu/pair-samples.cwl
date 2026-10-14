cwlVersion: v1.2
class: CommandLineTool
label: Pair R1/R2 FASTQ files in a sequencing run folder
doc: |
  Runs on a worker and lists the top level of a run folder given as an
  absolute path on the worker host. The folder must be under one of the
  worker's --extra-bind paths. The data is never copied: the step returns
  sample names and file basenames, and the profiling step reads the files
  in place.

  Recognised names (case-insensitive, .fastq/.fq, optionally .gz):
    <sample>_S<n>_R1_001.fastq.gz    (bcl2fastq / BCL Convert, no lane splitting)
    <sample>_S<n>_L001_R1_001.fastq.gz
    <sample>_R1.fastq.gz, <sample>.R1.fq.gz, <sample>_1.fastq.gz
  Other files (Undetermined_*, hidden files, reports, sample sheets) are ignored.
  Sample and project names keep only A-Z a-z 0-9 . _ - (other characters
  become _).
  Fails if a sample lacks its mate or has more than one R1 or R2
  (e.g. lane-split output: merge lanes first).

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: esviritu-1.3.4-db3.2.4.sif
  ResourceRequirement:
    coresMin: 1
    ramMin: 1000

hints:
  gowe:Execution:
    executor: worker

baseCommand: [python3, -I, -c]

arguments:
  - position: 0
    valueFrom: |-
      import json, os, re, sys
      run_path, project, undet = sys.argv[1], sys.argv[2], sys.argv[3] == "true"
      project = "" if project == "-" else project
      run_path = run_path.rstrip("/")
      if not os.path.isabs(run_path) or not run_path:
          sys.exit("run_path must be an absolute folder path, got %r" % sys.argv[1])
      if not os.path.isdir(run_path):
          sys.exit("run folder not found on this worker: %s (is it under an --extra-bind path?)" % run_path)
      rx = re.compile(r"^(.+?)(?:_S\d+)?(?:_L\d{3})?[._]R?([12])(?:_001)?\.(?:fastq|fq)(?:\.gz)?$", re.I)
      safe = lambda s: re.sub(r"[^A-Za-z0-9._-]", "_", s)
      def check_name(kind, s):
          if s in ("", ".", "..", "summary") or s.startswith("-"):
              sys.exit("invalid %s name %r: must not be empty, '.', '..', 'summary' or start with '-'" % (kind, s))
          return s
      by, raw = {}, {}
      for fn in sorted(os.listdir(run_path)):
          if fn.startswith(".") or not os.path.isfile(os.path.join(run_path, fn)):
              continue
          m = rx.match(fn)
          if not m:
              continue
          name = safe(m.group(1))
          if not undet and name.lower() == "undetermined":
              continue
          check_name("sample", name)
          if raw.setdefault(name, m.group(1)) != m.group(1):
              sys.exit("samples %r and %r both map to the name %s; rename one of them"
                       % (raw[name], m.group(1), name))
          mate = by.setdefault(name, {})
          if m.group(2) in mate:
              sys.exit("sample %s has more than one R%s file (%s, %s); merge lanes first"
                       % (name, m.group(2), mate[m.group(2)], fn))
          mate[m.group(2)] = fn
      if not by:
          sys.exit("no paired FASTQ files found in %s" % run_path)
      out = {"samples": [], "r1": [], "r2": [], "run_dir": run_path,
             "project": check_name("project", safe(project or os.path.basename(run_path)))}
      for name in sorted(by):
          mate = by[name]
          if "1" not in mate or "2" not in mate:
              sys.exit("sample %s is missing its R%s file" % (name, "2" if "1" in mate else "1"))
          out["samples"].append(name); out["r1"].append(mate["1"]); out["r2"].append(mate["2"])
      json.dump(out, open("cwl.output.json", "w"), indent=1)
      print("%d samples: %s" % (len(out["samples"]), ", ".join(out["samples"])))
  - position: 1
    valueFrom: $(inputs.run_path)
  - position: 2
    valueFrom: '$(inputs.project_name ? inputs.project_name : "-")'
  - position: 3
    valueFrom: '$(inputs.include_undetermined ? "true" : "false")'

inputs:
  run_path:
    type: string
    label: Run folder (absolute path on the worker host)
  project_name:
    type: string?
    label: Project name (default is the run folder name)
  include_undetermined:
    type: boolean
    default: false
    label: Also profile Undetermined reads

outputs:
  samples:
    type: string[]
  r1:
    type: string[]
  r2:
    type: string[]
  run_dir:
    type: string
  project:
    type: string
