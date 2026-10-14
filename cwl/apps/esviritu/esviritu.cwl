cwlVersion: v1.2
class: CommandLineTool
id: esviritu
label: EsViritu - viral detection and quantification from metagenomic reads
doc: |
  EsViritu (https://github.com/cmmr/EsViritu) detects, quantifies and
  taxonomically profiles human and animal viruses in metagenomic or
  virome reads by read mapping against a curated virus reference database.

  Image: esviritu-1.3.4-db3.2.4.sif (bioconda esviritu 1.3.4 plus reference
  DB v3.2.4 baked in at /opt/esviritu_db/v3.2.4; build recipe esviritu.def
  next to this file). Pass `database` to use a different DB directory.

  read_format: leave empty to infer it from the number of read files
  (1 file = unpaired, 2 files = paired). Set it to `interleaved` for one
  interleaved paired-end file.

  A sample in which no virus is found (EsViritu stops with "No reads aligned"
  or an empty coverage table) succeeds with no tables and writes
  <sample>/<sample>.no_viruses_detected.txt. Any other run that produces no
  results fails, with the tail of the EsViritu log.

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: esviritu-1.3.4-db3.2.4.sif
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: $(inputs.ram_mb)
  EnvVarRequirement:
    envDef:
      - envName: ESV_SAMPLE
        envValue: $(inputs.sample)

hints:
  gowe:Execution:
    executor: worker

baseCommand:
  - bash
  - -c
  - |-
    case "$ESV_SAMPLE" in
      ''|.|..|-*|*/*) echo "invalid sample name: '$ESV_SAMPLE'" >&2; exit 1;;
    esac
    EsViritu "$@"
    rc=$?
    o="$ESV_SAMPLE"
    log="$o/$ESV_SAMPLE"_esviritu.log
    if [ -s "$o/$ESV_SAMPLE.detected_virus.info.tsv" ]; then exit $rc; fi
    if [ -f "$log" ] && grep -qE "No reads aligned after|CoverM-like table is empty" "$log"; then
      echo "no viruses detected" > "$o/$ESV_SAMPLE.no_viruses_detected.txt"
      echo "EsViritu: no viruses detected in $ESV_SAMPLE" >&2
      exit 0
    fi
    echo "EsViritu produced no results for $ESV_SAMPLE (exit $rc)" >&2
    if [ -f "$log" ]; then tail -n 20 "$log" >&2; fi
    exit 1
  - esviritu

arguments:
  - prefix: --output_dir
    valueFrom: $(inputs.sample)
  - prefix: --working_directory
    valueFrom: $(runtime.outdir)
  - prefix: --cpu
    valueFrom: $(runtime.cores)
  - prefix: --read_format
    valueFrom: |-
      ${
        if (inputs.read_format) { return inputs.read_format; }
        if (inputs.reads.length == 2) { return "paired"; }
        if (inputs.reads.length == 1) { return "unpaired"; }
        throw "reads: expected 1 or 2 FASTQ files, got " + inputs.reads.length;
      }
  - prefix: --temp
    valueFrom: $(runtime.tmpdir)/esviritu_temp
  - prefix: --db
    valueFrom: '$(inputs.database ? inputs.database.path : "/opt/esviritu_db/v3.2.4")'
  - prefix: --qual
    valueFrom: '$(inputs.quality_filter ? "True" : "False")'
  - prefix: --dedup
    valueFrom: '$(inputs.dedup ? "True" : "False")'
  - prefix: --keep
    valueFrom: '$(inputs.keep_intermediate ? "True" : "False")'
  - prefix: --filter_seqs
    valueFrom: '$(inputs.filter_dir ? "True" : "False")'

inputs:
  reads:
    type: File[]
    label: Read files (FASTQ, optionally gzipped)
    doc: One file (unpaired or interleaved) or two files (R1 then R2).
    inputBinding:
      prefix: --reads
  sample:
    type: string
    label: Sample name
    doc: Prefix for all output files and name of the output folder. No spaces.
    inputBinding:
      prefix: --sample
  read_format:
    type:
      - "null"
      - type: enum
        symbols: [unpaired, paired, interleaved]
    label: Read format
    doc: Empty means 1 file = unpaired, 2 files = paired.
  minimap2_preset:
    type:
      type: enum
      symbols: [sr, map-hifi, "lr:hq", sense]
    default: sr
    label: minimap2 preset
    doc: sr = Illumina short reads, map-hifi = PacBio HiFi, lr:hq = Oxford Nanopore.
    inputBinding:
      prefix: --minimap2-preset
  quality_filter:
    type: boolean
    default: false
    label: Remove low-quality reads with fastp
  dedup:
    type: boolean
    default: false
    label: Remove PCR duplicates during fastp preprocessing
  species_threshold:
    type: float
    default: 0.90
    label: Minimum ANI for species-level classification
    inputBinding:
      prefix: --species-threshold
  subspecies_threshold:
    type: float
    default: 0.95
    label: Minimum ANI for subspecies-level classification
    inputBinding:
      prefix: --subspecies-threshold
  min_align_length:
    type: int
    default: 100
    label: Minimum alignment length (nt)
    doc: Only lower this for reads shorter than 100 nt; it affects the false-positive rate.
    inputBinding:
      prefix: --min-align-length
  minimap2_K:
    type: string
    default: 50M
    label: minimap2 mini-batch size (-K)
    doc: Lower it to reduce memory use.
    inputBinding:
      prefix: --minimap2-K
  keep_intermediate:
    type: boolean
    default: false
    label: Keep intermediate files (BAMs etc.)
  threads:
    type: int
    default: 4
    label: CPU threads
    doc: Cores reserved for the task and passed to EsViritu as --cpu.
  ram_mb:
    type: int
    default: 32000
    label: Memory (MB)
    doc: Memory reserved for the task. Lower minimap2_K if a sample runs out of memory.
  database:
    type: Directory?
    label: Custom EsViritu database directory
    doc: Leave empty to use the DB v3.2.4 bundled in the image.
  filter_dir:
    type: Directory?
    label: Host/spike-in filter directory
    doc: Directory containing filter_seqs.fna. When set, matching reads are removed before profiling.
    inputBinding:
      prefix: --filter_dir

outputs:
  output_dir:
    type: Directory
    label: All EsViritu outputs
    outputBinding:
      glob: $(inputs.sample)
  detected_virus_info:
    type: File?
    label: Per-contig detections
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).detected_virus.info.tsv
  assembly_summary:
    type: File?
    label: Per-assembly detections
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).detected_virus.assembly_summary.tsv
  tax_profile:
    type: File?
    label: Taxonomic profile
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).tax_profile.tsv
  coverage_windows:
    type: File?
    label: Coverage windows per reference
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).virus_coverage_windows.tsv
  report_html:
    type: File?
    label: Interactive HTML report
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample)_EsViritu_reactable.html
  no_viruses_detected:
    type: File?
    label: Marker written when no virus was detected
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).no_viruses_detected.txt
  consensus_fasta:
    type: File?
    label: Consensus sequences of detected viruses
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample)_final_consensus.fasta
  fastp_json:
    type: File?
    label: fastp read QC report (JSON)
    doc: Present only when quality_filter is on.
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).fastp.json
  fastp_html:
    type: File?
    label: fastp read QC report (HTML)
    doc: Present only when quality_filter is on.
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).fastp.html
