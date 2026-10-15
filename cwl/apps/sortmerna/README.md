# SortMeRNA rRNA content

`sortmerna.cwl` measures how much of a paired-end sample is ribosomal RNA with SortMeRNA 4.3 (`--paired_in`: a pair
counts when either mate passes the E-value threshold); `sortmerna-run.cwl` runs it on every sample of a run folder,
reusing `../esviritu/pair-samples.cwl`. `../silva-rrna/` measures the same thing faster with minimap2 against SILVA.

```yaml
# job.yml
run_path: /nfs/seq-data/nextseq2k_output/nextseq2k_runs/<run>/<fastq folder>   # real path, not a host symlink
reference: /nfs/seq-data/experiments/data/databases/sortmerna/4.3.4/smr_v4.3_default_db.fasta
index_dir: /nfs/seq-data/experiments/data/databases/sortmerna/4.3.4/idx_default
max_pairs: 2000000     # default; 0 = all pairs
threads: 16
```

```bash
gowe submit --workflow <sortmerna-run id> --group esviritu -i job.yml        # on GoWe workers
cwl-runner --runtime apptainer --image-dir /nfs/seq-data/images --extra-bind /nfs/seq-data \
  --parallel -j 4 sortmerna-run.cwl job.yml                                   # or locally
```

Outputs per sample: `<sample>.sortmerna.summary.tsv` (pairs analysed, rRNA pairs and %, pairs per SSU/LSU and domain,
5S, 5.8S), `<sample>.sortmerna.aligned.log` (SortMeRNA's summary) and `<sample>.sortmerna.log`. Aligned and
non-aligned reads are not kept.

Database and index (built once, read by every task)

```bash
curl -LO https://github.com/sortmerna/sortmerna/releases/download/v4.3.4/database.tar.gz && tar xzf database.tar.gz
sortmerna --ref $PWD/smr_v4.3_default_db.fasta --index 1 --idx-dir $PWD/idx_default --workdir /scratch/smr-idx
```
(`smr_v4.3_default_db.fasta`: SILVA 138 SSU NR99, SILVA 132 LSU, RFAM 5S and 5.8S; indexing takes about 8 min and
1.4 GB.)

Notes
- Image `sortmerna-4.3.6.sif` (`quay.io/biocontainers/sortmerna:4.3.6--h9ee0642_0`). Do not use the 4.3.7 build: it
  needs AVX-512 and dies with SIGILL (exit 132) on CPUs without it, such as AMD EPYC 7713.
- SortMeRNA is slow: about 2,300 pairs/s with 16 threads on rRNA-rich data, so 50M pairs would take about 6 h. The
  default `max_pairs: 2000000` (the first 2M pairs, about 15 min) is plenty for a percentage.
- Temporary files (read copies, key-value store, aligned reads) go to the task's tmp dir and are removed at the end.
