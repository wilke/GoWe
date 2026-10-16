# Kraken2

`kraken2.cwl` classifies one paired-end sample; `kraken2-run.cwl` runs it on every sample of a run folder,
reusing `../esviritu/pair-samples.cwl` (same pairing and naming rules as `esviritu-run`).

Typical use: find out what the reads are that a targeted profiler (EsViritu) leaves unassigned.

```yaml
# job.yml
run_path: /nfs/seq-data/nextseq2k_output/nextseq2k_runs/<run>/<fastq folder>   # real path, not a host symlink
database: /nfs/seq-data/experiments/data/databases/kraken2/k2_pluspf_16_GB_20260626   # path on the worker, read in place
threads: 8
```

```bash
gowe submit --workflow kraken2-run --group esviritu -i job.yml        # on GoWe workers
cwl-runner --runtime apptainer --image-dir /nfs/seq-data/images --extra-bind /nfs/seq-data \
  --parallel -j 4 kraken2-run.cwl job.yml                                    # or locally
```

Outputs per sample: `<sample>.kraken2.report.txt` (Kraken2 report with minimizer columns) and `<sample>.kraken2.log`.

Notes
- Image `kraken2_2.1.2-no-db.sif` (`docker://staphb/kraken2:2.1.2-no-db`, Kraken 2.1.2, sha256
  21fe8dce5e8d936a2b5781633def152566b6b72841cf8f88f5c43f92d11081c4) is resolved through `--image-dir`.
  `--report-minimizer-data` needs Kraken2 >= 2.1.0.
- `ram_mb` must exceed the database size: with Apptainer memory cgroups the memory-mapped database pages count
  against the task's limit (24000 for the 16 GB build, about 80000 for full PlusPF).
- `threads` x the number of workers on a host should not exceed the host's cores.
- The database is memory-mapped (`--memory-mapping`), so concurrent tasks on one host share it through the page cache;
  on first use it is read from NFS (about 50 s for a 16 GB database).
- Check `distinct minimizers` (column 5) before trusting a large count: thousands of reads with a handful of
  minimizers usually mean rRNA or another repeated sequence assigned to whichever genome in the database carries a
  copy (e.g. eukaryotic LSU rRNA reported as *Plasmodium relictum* in wastewater).
