# EsViritu

CWL for [EsViritu](https://github.com/cmmr/EsViritu), which detects, quantifies and profiles viruses in metagenomic reads.

| File | Registered as | Purpose |
|---|---|---|
| `esviritu.cwl` | `esviritu` | One sample: one or two FASTQ files |
| `esviritu-run.cwl` | `esviritu-run` | A whole sequencing run folder with two FASTQ files per sample |
| `pair-samples.cwl` | (step) | Lists the run folder on the worker and pairs R1/R2 by sample |
| `esviritu-summarize.cwl` | (step) | `summarize_esv_runs`: combined project tables and HTML report |
| `esviritu.def` | | Apptainer recipe for the image |

## Image

`esviritu-1.3.4-db3.2.4.sif` (1.1 GB, sha256 `a47da0a0de79253c0c26e16696b6ed563380565a4b16c8da2521146fd8139f12`) contains:
- bioconda `esviritu:1.3.4--pyhdfd78af_0`;
- reference DB v3.2.4 at `/opt/esviritu_db/v3.2.4` (Zenodo 17716199, md5 `24d85c1ec3cbffff12e921d2f39c91b2`);
- the vendored `dataui` R package. The biocontainer lacks it, so without it no HTML report is written.

The tools reference it by relative name. Every worker that runs them needs the file in its `--image-dir`.
On coconut it is at `/scout/containers/`.

To rebuild, run this from a directory that holds the unpacked DB as `db/v3.2.4`:

    apptainer build --fakeroot esviritu-1.3.4-db3.2.4.sif esviritu.def

## Run folders (`esviritu-run`)

`run_path` is an **absolute path on the worker host**. Every step runs on a worker, including the pairing step.
The reads are read in place and never copied, uploaded or seen by the server.
So the folder only has to be visible to the workers that run the job:
- it must be under one of their `--extra-bind` paths, as the real path: a host symlink into a bound folder
  (e.g. `/local/incoming` -> `/nfs/seq-data`) is not visible inside the container, so give the `/nfs/seq-data/...` path;
- pin the job to those workers with `--group`.

Anyone who can submit to that group can profile any FASTQ folder under the workers' `--extra-bind` paths,
so bind only the sequencing data the group's users may read.

    # run.yml
    run_path: /nfs/seq-data/RUN_241012_NB551234

    gowe submit --workflow esviritu-run -i run.yml --group esviritu

Steps:
1. **pair:** finds `<sample>_S<n>_R1_001.fastq.gz` / `_R2_` pairs (also `<sample>_R1.fastq.gz` and `<sample>_1.fq.gz`).
   - It skips hidden files, anything that is not FASTQ, and `Undetermined_*` (set `include_undetermined: true` to profile them).
   - Sample and project names keep only `A-Z a-z 0-9 . _ -`; other characters become `_`.
   - It fails if a sample lacks its mate, still has lane-split files (merge lanes first), two names collide after
     that cleanup, or a name starts with `-`.
2. **profile:** runs EsViritu once per sample, in parallel. Defaults suit NextSeq paired-end data: fastp filtering is on, which also trims poly-G tails.
3. **summarize:** builds the project-level detection, assembly, taxonomy, coverage and read-stats tables plus one HTML report.

A sample with no detectable virus (for example a negative control) succeeds:
- it writes `<sample>.no_viruses_detected.txt` instead of tables;
- it still appears in the project read stats.

Any other EsViritu failure fails the task and shows the tail of the EsViritu log. This includes a missing input or a missing DB, which EsViritu itself exits 0 on.

Resources: each sample reserves `threads` cores (default 4, also EsViritu's `--cpu`) and `ram_mb` MB (default 32000).
The memory default comes from a NextSeq run where a 208M-read sample peaked at 23.6 GB in the
single-threaded coverage-table step; smaller samples used 9-13 GB. Both are inputs of `esviritu` and
`esviritu-run`, so they can be set per submission:

    threads: 16
    ram_mb: 48000

## Single sample (`esviritu`)

    gowe submit --workflow esviritu -i job.example.yml

Local test with the standalone runner:

    cwl-runner --runtime apptainer --image-dir /scout/containers esviritu.cwl job.example.yml

`cwl-runner` currently ignores `--image-dir` for steps inside a workflow. To test `esviritu-run.cwl` locally, run it from the directory that holds the `.sif`.
