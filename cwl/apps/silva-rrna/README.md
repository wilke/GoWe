# SILVA rRNA content

`silva-rrna.cwl` measures how much of a paired-end sample is ribosomal RNA by mapping it with minimap2 against SILVA
SSU + LSU NR99; `silva-rrna-run.cwl` runs it on every sample of a run folder, reusing `../esviritu/pair-samples.cwl`
(same pairing and naming rules as `esviritu-run`). `../sortmerna/` measures the same thing with SortMeRNA.

A pair counts as rRNA when at least one mate has an alignment of at least `min_aln_len` nt (default 50) with at least
`min_identity` identity (default 0.85). Each rRNA pair is assigned to the gene and lineage of its first passing alignment.

```yaml
# job.yml
run_path: /nfs/seq-data/nextseq2k_output/nextseq2k_runs/<run>/<fastq folder>   # real path, not a host symlink
index: /nfs/seq-data/experiments/data/databases/silva/138.2/silva_138.2_nr99_ssu_lsu.sr.mmi
taxonomy: /nfs/seq-data/experiments/data/databases/silva/138.2/silva_138.2_nr99_ssu_lsu.taxonomy.tsv
threads: 16
```

```bash
gowe submit --workflow <silva-rrna-run id> --group esviritu -i job.yml        # on GoWe workers
cwl-runner --runtime apptainer --image-dir /nfs/seq-data/images --extra-bind /nfs/seq-data \
  --parallel -j 4 silva-rrna-run.cwl job.yml                                    # or locally
```

Outputs per sample
- `<sample>.silva.summary.tsv`: pairs analysed, rRNA pairs and %, pairs per gene (SSU/LSU) and domain; `Organelle`
  = SILVA Chloroplast and Mitochondria lineages (they sit inside Bacteria in SILVA)
- `<sample>.silva.taxa.tsv`: rRNA pairs per gene and lineage (first 6 ranks)
- `<sample>.silva.minimap2.log`

Building the reference (SILVA 138.2, about 1 min; the index needs 5.5 GB RAM to build and 2.5 GB to use)

```bash
for p in SSU LSU; do
  zcat SILVA_138.2_${p}Ref_NR99_tax_silva.fasta.gz | awk -v P=$p 'BEGIN{OFS="\t"}
    /^>/ {id = P "|" substr($1, 2); print id, P, substr($0, length($1) + 2) > "/dev/stderr"; print ">" id; next}
    {gsub(/U/, "T"); gsub(/u/, "t"); print}'
done > silva_138.2_nr99_ssu_lsu.fasta 2> silva_138.2_nr99_ssu_lsu.taxonomy.tsv
minimap2 -x sr -d silva_138.2_nr99_ssu_lsu.sr.mmi silva_138.2_nr99_ssu_lsu.fasta
```
IDs get an `SSU|`/`LSU|` prefix because some accessions occur in both files. SILVA is free for academic use
(https://www.arb-silva.de/silva-license-information/).

Notes
- Image `minimap2_latest.sif` (minimap2 2.29) is resolved through `--image-dir`.
- Speed: about 16,000 pairs/s with 16 threads (50M pairs in about 50 min). `max_pairs` (default 0 = all) limits the
  run to the first N pairs.
- On a 200,000-pair sample of wastewater RNA (118660) the thresholds give 82% rRNA pairs; any alignment of 30 nt or
  more gives 86%, and SortMeRNA 90%. The difference is mostly divergent eukaryotic LSU that matches SILVA only partly.
