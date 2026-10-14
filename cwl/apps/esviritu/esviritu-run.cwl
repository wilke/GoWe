cwlVersion: v1.2
class: Workflow
id: esviritu-run
label: EsViritu - virus profiling for a sequencing run folder
doc: |
  Runs EsViritu on every sample in a run folder (two FASTQ files per sample,
  e.g. NextSeq output from bcl2fastq / BCL Convert without lane splitting).
  Samples run in parallel. A final step combines them into project-level
  tables and an HTML report (summarize_esv_runs).

  run_path is an absolute path on the worker host and must be under one of
  the worker's --extra-bind paths. Everything runs on workers, and the reads
  are read in place, never copied or uploaded. Submit with --group <group>
  to pin the run to the workers that sit next to the data
  (e.g. --group esviritu).

  Defaults suit Illumina NextSeq paired-end data: fastp quality filtering is
  on, which also trims NextSeq poly-G tails.

requirements:
  ScatterFeatureRequirement: {}
  InlineJavascriptRequirement: {}
  StepInputExpressionRequirement: {}

inputs:
  run_path:
    type: string
    label: Run folder, absolute path on the worker host (two FASTQ files per sample)
  project_name:
    type: string?
    label: Project name (default is the run folder name)
  include_undetermined:
    type: boolean
    default: false
    label: Also profile Undetermined reads
  quality_filter:
    type: boolean
    default: true
    label: fastp quality filtering (includes NextSeq poly-G trimming)
  dedup:
    type: boolean
    default: false
    label: Remove PCR duplicates
  min_align_length:
    type: int
    default: 100
    label: Minimum alignment length (nt); lower it only for reads shorter than 100 nt
  species_threshold:
    type: float
    default: 0.90
    label: Minimum ANI for species-level classification
  subspecies_threshold:
    type: float
    default: 0.95
    label: Minimum ANI for subspecies-level classification
  filter_dir:
    type: Directory?
    label: Host/spike-in filter directory (contains filter_seqs.fna)
  threads:
    type: int
    default: 4
    label: CPU threads per sample
  ram_mb:
    type: int
    default: 32000
    label: Memory per sample (MB)

steps:
  pair:
    run: pair-samples.cwl
    in:
      run_path: run_path
      project_name: project_name
      include_undetermined: include_undetermined
    out: [samples, r1, r2, run_path, project]

  profile:
    run: esviritu.cwl
    scatter: [sample, r1, r2]
    scatterMethod: dotproduct
    in:
      sample: pair/samples
      r1: pair/r1
      r2: pair/r2
      run_path: pair/run_path
      reads:
        valueFrom: |-
          ${
            var dir = inputs.run_path + "/";
            return [inputs.r1, inputs.r2].map(function (name) {
              return {"class": "File", "location": "file://" + dir + name,
                      "path": dir + name, "basename": name};
            });
          }
      read_format:
        default: paired
      quality_filter: quality_filter
      dedup: dedup
      min_align_length: min_align_length
      species_threshold: species_threshold
      subspecies_threshold: subspecies_threshold
      filter_dir: filter_dir
      threads: threads
      ram_mb: ram_mb
    out: [output_dir, tax_profile, report_html, fastp_json, fastp_html]

  summarize:
    run: esviritu-summarize.cwl
    in:
      project: pair/project
      sample_dirs: profile/output_dir
    out: [summary_dir, detected_virus_info, assembly_summary, tax_profile, readstats, report_html]

outputs:
  samples:
    type: string[]
    outputSource: pair/samples
  sample_results:
    type: Directory[]
    label: Per-sample EsViritu outputs
    outputSource: profile/output_dir
  project_summary:
    type: Directory
    label: Combined tables and report
    outputSource: summarize/summary_dir
  project_detected_viruses:
    type: File?
    outputSource: summarize/detected_virus_info
  project_tax_profile:
    type: File?
    outputSource: summarize/tax_profile
  project_report:
    type: File?
    outputSource: summarize/report_html
  fastp_json:
    type: File?[]
    label: Per-sample fastp QC reports (JSON); null where quality_filter is off
    outputSource: profile/fastp_json
  fastp_html:
    type: File?[]
    label: Per-sample fastp QC reports (HTML); null where quality_filter is off
    outputSource: profile/fastp_html
