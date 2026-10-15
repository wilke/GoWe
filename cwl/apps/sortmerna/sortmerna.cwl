cwlVersion: v1.2
class: CommandLineTool
id: sortmerna
label: SortMeRNA - rRNA content of a paired-end sample
doc: |
  Runs SortMeRNA 4.3 on one sample's read pairs against an rRNA reference
  (e.g. smr_v4.3_default_db.fasta: SILVA 138 SSU NR99, SILVA 132 LSU,
  RFAM 5S and 5.8S) with --paired_in, and counts the pairs in which at least
  one mate passes SortMeRNA's E-value threshold. Each rRNA pair is assigned
  to the database part (SSU/LSU/5S/5.8S) and domain of its first mate's best
  alignment.

  Writes
    <sample>.sortmerna.summary.tsv   one row: pairs analysed, rRNA pairs and %,
                                     pairs per gene and domain
    <sample>.sortmerna.aligned.log   SortMeRNA's own summary
    <sample>.sortmerna.log           SortMeRNA console output
  The aligned and non-aligned reads are not kept.

  `reference` and `index_dir` are paths on the worker host, read in place.
  Build the index once (sortmerna --index 1, see README.md) so tasks do not
  rebuild it; tasks only read it.

  SortMeRNA is slow (about 2,300 read pairs/s with 16 threads on rRNA-rich
  NextSeq data), so `max_pairs` defaults to the first 2,000,000 pairs of
  each sample (from the first tiles of the flow cell). Set it to 0 for all.

  Image: sortmerna-4.3.6.sif (biocontainers sortmerna:4.3.6--h9ee0642_0;
  the 4.3.7 build uses AVX-512 and dies with SIGILL on AMD EPYC 7713).

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: sortmerna-4.3.6.sif
  ResourceRequirement:
    coresMin: $(inputs.threads)
    ramMin: $(inputs.ram_mb)

hints:
  gowe:Execution:
    executor: worker

baseCommand:
  - bash
  - -c
  - |-
    set -euo pipefail
    sample=$1 ref=$2 idx=$3 threads=$4 maxp=$5 tmp=$6 r1=$7 r2=$8
    case "$sample" in
      ''|.|..|-*|*/*) echo "invalid sample name: '$sample'" >&2; exit 1;;
    esac
    for f in "$ref" "$idx" "$r1" "$r2"; do
      [ -r "$f" ] || { echo "not readable on this worker: $f" >&2; exit 1; }
    done
    rm -rf "$tmp"; mkdir -p "$tmp"
    if [ "$maxp" -gt 0 ]; then
      { zcat -f "$r1" || true; } | head -n $((maxp * 4)) > "$tmp/sub_R1.fq"
      { zcat -f "$r2" || true; } | head -n $((maxp * 4)) > "$tmp/sub_R2.fq"
      r1=$tmp/sub_R1.fq r2=$tmp/sub_R2.fq
    fi
    sortmerna --ref "$ref" --idx-dir "$idx" --reads "$r1" --reads "$r2" \
      --paired_in --threads "$threads" --workdir "$tmp/smr" \
      --blast 1 --num_alignments 1 > "$sample.sortmerna.log" 2>&1 \
      || { tail -n 20 "$sample.sortmerna.log" >&2; exit 1; }
    touch "$tmp/smr/out/aligned.blast"   # absent when no read aligns
    cp "$tmp/smr/out/aligned.log" "$sample.sortmerna.aligned.log"
    reads=$(awk -F'= *' '/Total reads =/ { print $2 + 0; exit }' "$sample.sortmerna.aligned.log")
    awk -F'\t' -v OFS='\t' -v S="$sample" -v P="$((reads / 2))" -v M="$maxp" -v DB="$ref" '
      FNR == NR {
        if (substr($0, 1, 1) == ">") {
          split(substr($0, 2), h, " "); lin = substr($0, length(h[1]) + 3)
          split(lin, r, ";"); dom[h[1]] = (h[1] ~ /^SILVA/ && r[1] != "" ? r[1] : "all")
        }
        next
      }
      $1 == last { next }
      {
        last = $1; tot++
        g = ($2 ~ /SSURef/ ? "SSU" : $2 ~ /LSURef/ ? "LSU" : $2 ~ /5\.8S/ ? "5.8S" : $2 ~ /5S/ ? "5S" : "other")
        c[g "_" dom[$2]]++
      }
      END {
        split("SSU_Bacteria SSU_Archaea SSU_Eukaryota LSU_Bacteria LSU_Archaea LSU_Eukaryota 5S_all 5.8S_all", k, " ")
        h = "sample\tpairs_analysed\trrna_pairs\trrna_pct"; v = S "\t" P "\t" (tot + 0) "\t" (P ? sprintf("%.2f", 100 * tot / P) : "NA")
        for (i = 1; i <= 8; i++) { h = h "\t" k[i]; v = v "\t" (c[k[i]] + 0) }
        print h, "max_pairs", "database"; print v, M, DB
      }' "$ref" "$tmp/smr/out/aligned.blast" > "$sample.sortmerna.summary.tsv"
    rm -rf "$tmp"
    cat "$sample.sortmerna.summary.tsv"
  - sortmerna-rrna

arguments:
  - position: 1
    valueFrom: $(inputs.sample)
  - position: 2
    valueFrom: $(inputs.reference)
  - position: 3
    valueFrom: $(inputs.index_dir)
  - position: 4
    valueFrom: $(runtime.cores)
  - position: 5
    valueFrom: $(inputs.max_pairs)
  - position: 6
    valueFrom: $(runtime.tmpdir)/sortmerna
  - position: 7
    valueFrom: $(inputs.reads[0].path)
  - position: 8
    valueFrom: |-
      ${
        if (inputs.reads.length != 2) { throw "reads: expected R1 and R2, got " + inputs.reads.length + " files"; }
        return inputs.reads[1].path;
      }

inputs:
  sample:
    type: string
    doc: Sample name used for output file names. No spaces or slashes.
  reads:
    type: File[]
    doc: R1 and R2 FASTQ files (optionally gzipped).
  reference:
    type: string
    doc: Absolute path on the worker host of the rRNA reference FASTA (e.g. smr_v4.3_default_db.fasta).
  index_dir:
    type: string
    doc: Absolute path on the worker host of the SortMeRNA index directory built from `reference`.
  max_pairs:
    type: int
    default: 2000000
    doc: Analyse only the first N pairs; 0 = all (hours for 50M pairs).
  threads:
    type: int
    default: 8
  ram_mb:
    type: int
    default: 16000

outputs:
  summary:
    type: File
    outputBinding:
      glob: $(inputs.sample).sortmerna.summary.tsv
  aligned_log:
    type: File
    outputBinding:
      glob: $(inputs.sample).sortmerna.aligned.log
  log:
    type: File
    outputBinding:
      glob: $(inputs.sample).sortmerna.log
