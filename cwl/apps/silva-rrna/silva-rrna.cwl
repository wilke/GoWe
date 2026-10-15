cwlVersion: v1.2
class: CommandLineTool
id: silva-rrna
label: rRNA content of a paired-end sample by mapping to SILVA SSU+LSU
doc: |
  Maps the read pairs of one sample with minimap2 (-x sr, base-level
  alignment) against SILVA NR99 SSU and LSU rRNA and counts the pairs in
  which at least one mate has an alignment of at least `min_aln_len` nt with
  at least `min_identity` identity. Each rRNA pair is assigned to the gene
  (SSU/LSU) and lineage of its first passing alignment.

  Writes
    <sample>.silva.summary.tsv  one row: pairs analysed, rRNA pairs and %,
                                pairs per gene and domain (organelle =
                                SILVA Chloroplast or Mitochondria lineages)
    <sample>.silva.taxa.tsv     rRNA pairs per gene and lineage (first 6 ranks)
    <sample>.silva.minimap2.log

  The reference is a path on the worker host (read in place, not staged):
  `index` is a minimap2 -x sr index of the SILVA FASTA (U converted to T,
  IDs prefixed "SSU|" / "LSU|") and `taxonomy` is a TSV of ID, gene, lineage.
  Build recipe: README.md next to this file.

  `max_pairs` > 0 analyses only the first N pairs of the files (from the
  first tiles of the flow cell), which is enough for a fraction estimate.

  Image: minimap2_latest.sif (minimap2 2.29), resolved via the worker's --image-dir.

$namespaces:
  gowe: https://github.com/wilke/GoWe#

requirements:
  InlineJavascriptRequirement: {}
  DockerRequirement:
    dockerPull: minimap2_latest.sif
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
    sample=$1 index=$2 tax=$3 threads=$4 maxp=$5 minlen=$6 minid=$7 tmp=$8 r1=$9 r2=${10}
    case "$sample" in
      ''|.|..|-*|*/*) echo "invalid sample name: '$sample'" >&2; exit 1;;
    esac
    for f in "$index" "$tax" "$r1" "$r2"; do
      [ -r "$f" ] || { echo "not readable on this worker: $f" >&2; exit 1; }
    done
    if [ "$maxp" -gt 0 ]; then
      mkdir -p "$tmp"
      { zcat -f "$r1" || true; } | head -n $((maxp * 4)) > "$tmp/sub_R1.fq"
      { zcat -f "$r2" || true; } | head -n $((maxp * 4)) > "$tmp/sub_R2.fq"
      r1=$tmp/sub_R1.fq r2=$tmp/sub_R2.fq
    fi
    log=$sample.silva.minimap2.log
    minimap2 -c -x sr -t "$threads" --secondary=no "$index" "$r1" "$r2" 2> "$log" \
    | awk -F'\t' -v OFS='\t' -v L="$minlen" -v I="$minid" -v TAX="$tax" \
          -v CAT="$tmp.cat" -v TX="$sample.silva.taxa.tsv" '
      BEGIN {
        while ((getline line < TAX) > 0) { split(line, a, "\t"); tx[a[1]] = a[3] }
        close(TAX)
      }
      { q = $1; sub(/\/[12]$/, "", q) }
      q == last { next }
      $11 < L || $10 / $11 < I { next }
      {
        last = q
        gene = substr($6, 1, 3); t = tx[$6]
        n = split(t, r, ";"); dom = (n ? r[1] : "unknown")
        if (t ~ /;Chloroplast(;|$)|;Mitochondria(;|$)/) dom = "Organelle"
        cat[gene "_" dom]++
        lin = r[1]; for (i = 2; i <= n && i <= 6; i++) lin = lin ";" r[i]
        taxa[gene "\t" lin]++
      }
      END {
        for (k in cat) print k, cat[k] > CAT
        close(CAT)
        print "pairs", "gene", "lineage" > TX
        close(TX)
        for (k in taxa) print taxa[k], k | "sort -t \"\t\" -k1,1nr >> " TX
      }'
    touch "$tmp.cat"
    pairs=$(awk '/mapped [0-9]+ sequences/ { for (i = 1; i <= NF; i++) if ($i == "mapped") s += $(i + 1) }
                 END { printf "%d", s / 2 }' "$log")
    awk -F'\t' -v OFS='\t' -v S="$sample" -v P="$pairs" -v M="$maxp" -v DB="$index" '
      { c[$1] = $2; tot += $2 }
      END {
        split("SSU_Bacteria SSU_Archaea SSU_Eukaryota SSU_Organelle LSU_Bacteria LSU_Archaea LSU_Eukaryota LSU_Organelle", k, " ")
        h = "sample\tpairs_analysed\trrna_pairs\trrna_pct"; v = S "\t" P "\t" tot "\t" (P ? sprintf("%.2f", 100 * tot / P) : "NA")
        for (i = 1; i <= 8; i++) { h = h "\t" k[i]; v = v "\t" (c[k[i]] + 0) }
        print h, "max_pairs", "database"; print v, M, DB
      }' "$tmp.cat" > "$sample.silva.summary.tsv"
    rm -f "$tmp.cat" "$tmp/sub_R1.fq" "$tmp/sub_R2.fq"
    cat "$sample.silva.summary.tsv"
  - silva-rrna

arguments:
  - position: 1
    valueFrom: $(inputs.sample)
  - position: 2
    valueFrom: $(inputs.index)
  - position: 3
    valueFrom: $(inputs.taxonomy)
  - position: 4
    valueFrom: $(runtime.cores)
  - position: 5
    valueFrom: $(inputs.max_pairs)
  - position: 6
    valueFrom: $(inputs.min_aln_len)
  - position: 7
    valueFrom: $(inputs.min_identity)
  - position: 8
    valueFrom: $(runtime.tmpdir)/silva-rrna
  - position: 9
    valueFrom: $(inputs.reads[0].path)
  - position: 10
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
  index:
    type: string
    doc: Absolute path on the worker host of the minimap2 -x sr index of the SILVA SSU+LSU FASTA (.mmi).
  taxonomy:
    type: string
    doc: Absolute path on the worker host of the ID / gene / lineage TSV that belongs to the index.
  max_pairs:
    type: int
    default: 0
    doc: Analyse only the first N pairs; 0 = all.
  min_aln_len:
    type: int
    default: 50
    doc: Minimum alignment block length (nt) for a mate to count as rRNA.
  min_identity:
    type: float
    default: 0.85
    doc: Minimum alignment identity (matching bases / block length).
  threads:
    type: int
    default: 8
  ram_mb:
    type: int
    default: 8000
    doc: Memory per task (MB); the index takes about 2.5 GB.

outputs:
  summary:
    type: File
    outputBinding:
      glob: $(inputs.sample).silva.summary.tsv
  taxa:
    type: File
    outputBinding:
      glob: $(inputs.sample).silva.taxa.tsv
  log:
    type: File
    outputBinding:
      glob: $(inputs.sample).silva.minimap2.log
