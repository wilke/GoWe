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
- it must be under one of their `--extra-bind` paths;
- pin the job to those workers with `--group`.

    # run.yml
    run_path: /nfs/seq-data/RUN_241012_NB551234

    gowe submit --workflow esviritu-run -i run.yml --group anlseq

Steps:
1. **pair:** finds `<sample>_S<n>_R1_001.fastq.gz` / `_R2_` pairs (also `<sample>_R1.fastq.gz` and `<sample>_1.fq.gz`).
   - It skips `Undetermined_*` and anything that is not FASTQ.
   - It fails if a sample lacks its mate or still has lane-split files (merge lanes first).
2. **profile:** runs EsViritu once per sample, in parallel. Defaults suit NextSeq paired-end data: fastp filtering is on, which also trims poly-G tails.
3. **summarize:** builds the project-level detection, assembly, taxonomy, coverage and read-stats tables plus one HTML report.

A sample with no detectable virus (for example a negative control) succeeds:
- it writes `<sample>.no_viruses_detected.txt` instead of tables;
- it still appears in the project read stats.

Any other EsViritu failure fails the task and shows the tail of the EsViritu log. This includes a missing input or a missing DB, which EsViritu itself exits 0 on.

## Single sample (`esviritu`)

    gowe submit --workflow esviritu -i job.example.yml

Local test with the standalone runner:

    cwl-runner --runtime apptainer --image-dir /scout/containers esviritu.cwl job.example.yml

`cwl-runner` currently ignores `--image-dir` for steps inside a workflow. To test `esviritu-run.cwl` locally, run it from the directory that holds the `.sif`.
