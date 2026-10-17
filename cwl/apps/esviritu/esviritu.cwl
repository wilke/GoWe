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

  Duplicates: when viruses are detected, the primary alignments of
  EsViritu's final BAM (third.filt.sorted.bam) are duplicate-marked with
  samtools markdup (pairs by both mates' positions; optical duplicates within
  `optical_distance` pixels, 2500 suits patterned flow cells such as NextSeq
  2000 / NovaSeq). <sample>.virus_fragments.tsv lists per reference the
  primary reads, duplicates, optical duplicates and unique fragments (distinct
  read names among non-duplicate reads), so a detection supported by many
  reads from few molecules can be told apart from one with many independent
  fragments. EsViritu's own tables are unchanged.

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
      - envName: ESV_KEEP
        envValue: '$(inputs.keep_intermediate ? "True" : "False")'
      - envName: ESV_OPTDIST
        envValue: $(inputs.optical_distance)
      - envName: ESV_MINFRAG
        envValue: $(inputs.min_fragments)

hints:
  gowe:Execution:
    executor: worker

baseCommand:
  - bash
  - -c
  - |-
    set -o pipefail
    case "$ESV_SAMPLE" in
      ''|.|..|-*|*/*) echo "invalid sample name: '$ESV_SAMPLE'" >&2; exit 1;;
    esac
    # temp dir, cores and DB as passed to EsViritu (--temp, --cpu, --db)
    ESV_TEMP= ESV_CPU=1 ESV_DB= prev=
    for a in "$@"; do
      case "$prev" in --temp) ESV_TEMP=$a;; --cpu) ESV_CPU=$a;; --db) ESV_DB=$a;; esac
      prev=$a
    done
    export ESV_META="$ESV_DB/virus_pathogen_database.all_metadata.tsv"
    cleanup() { if [ "$ESV_KEEP" != True ] && [ -n "$ESV_TEMP" ]; then rm -rf "$ESV_TEMP"; fi; }
    # EsViritu always keeps its temp dir here, so the final BAM is still there
    # for the duplicate counts; cleanup() removes it unless keep_intermediate.
    EsViritu "$@" --keep True
    rc=$?
    o="$ESV_SAMPLE"
    log="$o/$ESV_SAMPLE"_esviritu.log
    if [ -s "$o/$ESV_SAMPLE.detected_virus.info.tsv" ]; then
      bam="$ESV_TEMP/$ESV_SAMPLE.third.filt.sorted.bam"
      if [ "$rc" -ne 0 ]; then
        echo "EsViritu exited $rc for $ESV_SAMPLE" >&2; exit "$rc"
      fi
      if [ ! -s "$bam" ]; then
        echo "EsViritu detected viruses in $ESV_SAMPLE but its final BAM $bam is missing; cannot count duplicates" >&2
        exit 1
      fi
      frag="$o/$ESV_SAMPLE.virus_fragments.tsv"
      ESV_WEIGHTS="$ESV_TEMP/$ESV_SAMPLE.third.read_weights.tsv"
      [ -s "$ESV_WEIGHTS" ] || ESV_WEIGHTS=
      export ESV_WEIGHTS
      t="$ESV_TEMP/markdup"; mkdir -p "$t"
      if ! { samtools view -u -F 0x904 "$bam" \
             | samtools collate -O -u - "$t/collate" \
             | samtools fixmate -m -u - - \
             | samtools sort -u -m 1G -@ "$ESV_CPU" -T "$t/sort" - \
             | samtools markdup -d "$ESV_OPTDIST" -@ "$ESV_CPU" -f "$o/$ESV_SAMPLE.markdup.stats.txt" - "$t/md.bam"; } \
           || ! samtools view "$t/md.bam" | awk -F '\t' -v OFS='\t' '
             # With --collapse-duplicates each record stands for as many reads as its
             # weight (third.read_weights.tsv); every copy beyond the non-duplicate
             # records is a duplicate. optical_duplicates then only covers distinct reads.
             BEGIN { S = ENVIRON["ESV_SAMPLE"]; META = ENVIRON["ESV_META"]; MINF = ENVIRON["ESV_MINFRAG"] + 0
                     while ((getline l < META) > 0) { split(l, m, "\t"); sub_[m[1]] = m[12] }
                     W = ENVIRON["ESV_WEIGHTS"]
                     if (W != "") while ((getline l < W) > 0) { split(l, m, "\t"); w[m[1]] = m[2] } }
             { r = $3; tot[r] += (W != "" ? w[$1] : 1)
               if (int($2 / 1024) % 2) { if ($0 ~ /\tdt:Z:SQ/) opt[r]++ }
               else { nd[r]++; if (!seen[$1 "\t" r]++) uf[r]++ } }
             END {
               print "sample_ID", "Accession", "subspecies", "primary_reads", "duplicates",
                     "optical_duplicates", "unique_fragments", "duplicate_pct", "low_support"
               for (r in tot) { d = tot[r] - nd[r]
                 printf "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%.1f\t%s\n", S, r, sub_[r], tot[r], d,
                        opt[r] + 0, uf[r] + 0, 100 * d / tot[r], (uf[r] + 0 < MINF ? "true" : "false") }
             }' > "$frag.tmp"; then
        echo "duplicate marking failed for $ESV_SAMPLE (temp dir kept: $ESV_TEMP)" >&2; exit 1
      fi
      { head -n 1 "$frag.tmp"; tail -n +2 "$frag.tmp" | sort -t "$(printf '\t')" -k4,4nr; } > "$frag"
      rm -f "$frag.tmp"
      cleanup; exit $rc
    fi
    if [ -f "$log" ] && grep -qE "No reads aligned after|CoverM-like table is empty" "$log"; then
      echo "no viruses detected" > "$o/$ESV_SAMPLE.no_viruses_detected.txt"
      echo "EsViritu: no viruses detected in $ESV_SAMPLE" >&2
      cleanup; exit 0
    fi
    echo "EsViritu produced no results for $ESV_SAMPLE (exit $rc; temp dir kept: $ESV_TEMP)" >&2
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
  # passed only when on, so images without the option (stock EsViritu 1.3.4) still work
  - valueFrom: '$(inputs.collapse_duplicates ? ["--collapse-duplicates", "True"] : [])'
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
  collapse_duplicates:
    type: boolean
    default: false
    label: Process each distinct read pair once, with its copy number as weight
    doc: >-
      Needs an EsViritu image with --collapse-duplicates (wilke/EsViritu v1.3.4-anl.p3 or
      later; not stock 1.3.4). Identical reads (pairs) are collapsed to one representative
      that is mapped and processed once, like --dedup; read_count and RPKMF then count
      distinct reads, and read_count_weighted / RPKMF_weighted count all reads.
      virus_fragments.tsv uses the copy numbers for primary_reads and duplicates.
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
    doc: Kept in the task's tmp dir (EsViritu --temp); otherwise removed when the task ends.
  min_fragments:
    type: int
    default: 50
    label: Unique fragments below which a reference is flagged low_support in virus_fragments.tsv
  optical_distance:
    type: int
    default: 2500
    label: Optical duplicate distance (pixels) for samtools markdup
    doc: 2500 for patterned flow cells (NextSeq 1000/2000, NovaSeq); about 100 for non-patterned ones (NextSeq 500/550, HiSeq 2500, MiSeq).
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
  exemplar_ties:
    type: File?
    label: Representative references chosen only by name order among tied references
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).exemplar_ties.tsv
  virus_fragments:
    type: File?
    label: Primary reads, duplicates and unique fragments per reference
    outputBinding:
      glob: $(inputs.sample)/$(inputs.sample).virus_fragments.tsv
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
