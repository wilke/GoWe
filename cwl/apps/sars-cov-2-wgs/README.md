# SARS-CoV-2 WGS paired-end variant calling (IWC port)

CWL port of the Galaxy IWC workflow **"COVID-19: variation analysis on WGS PE data"**, release 0.2.7
(MIT, author Wolfgang Maier; this folder keeps the MIT notice in [`LICENSE`](LICENSE);
[iwc/workflows/sars-cov-2-variant-calling/sars-cov-2-pe-illumina-wgs-variant-calling](https://github.com/galaxyproject/iwc/tree/main/workflows/sars-cov-2-variant-calling/sars-cov-2-pe-illumina-wgs-variant-calling)).
It is the first Galaxy-to-GoWe port, chosen to measure porting effort before the generic viral consensus workflow.

`sars-cov-2-wgs-pe.cwl` runs `sars-cov-2-wgs-sample.cwl` on every sample in parallel and builds one MultiQC report:

```
fastp -> bwa mem -> samtools sort + view (-q 20 -f 3) -> picard MarkDuplicates (remove)
      -> lofreq viterbi (+ sort) -> lofreq indelqual (dindel) -> lofreq call (--call-indels)
      -> lofreq filter (strand bias FDR, flag only) -> snpEff (covid19 build, NC_045512.2)
samtools stats on the filtered BAM; MultiQC over fastp, samtools stats and MarkDuplicates
```

Each tool's `doc` lists the Galaxy tool version and the parameters it reproduces; the flags were taken from the
IWC `.ga` file and the Galaxy wrappers in tools-iuc / tools-devteam.

## Run

```bash
gowe submit sars-cov-2-wgs-pe.cwl -i job.yml                                   # on GoWe workers
cwl-runner --runtime apptainer --parallel sars-cov-2-wgs-pe.cwl job.yml        # or locally
```

`job.yml` (see `job.example.yml`): `samples` is a list of `{name, forward, reverse}`, `reference` must be
NC_045512.2 (snpEff uses its bundled NC_045512.2 database and chromosome names must match), `threads` defaults to 2.

Outputs: per sample `<name>.snpeff.vcf` (annotated; strand-biased records kept with FILTER `sb_fdr`),
`<name>.lofreq.filtered.vcf`, `<name>.lofreq.vcf`, `<name>.indelqual.bam` (+ `.bai`), `<name>.fastp.html`,
`<name>.snpeff.html`, `<name>.markdup_metrics.txt` (Picard) and the samtools stats file; plus
`preprocessing_and_mapping_report.html` for all samples.

## Validation against the IWC test

IWC test data: reference `https://zenodo.org/record/4555735/files/NC_045512.2_reference.fasta`
(SHA-1 db3759c2e1d9ce8827ba4aa1749e759313591240), reads
`https://zenodo.org/records/10174466/files/SRR11578257_R{1,2}.fastq.gz`
(SHA-1 db2f908fbbb6c4920ee0de73265acdeb92c9895f / 4f754a7023bcc3cb1a240d3fb03532338c5c1107).

Result with `cwl-runner` 0.26.1 (Apptainer, 2026-11-10, 1 min 46 s wall on coconut), against the assertions in
`pe-wgs-variation-tests.yml`:

| Assertion | IWC expects | This port |
|---|---|---|
| `##SnpEffVersion="4.5covid19` header | present | present |
| variant records | 55 ± 3 | 55 |
| FILTER PASS | 39 ± 3 | 39 |
| FILTER sb_fdr | 16 ± 3 | 16 |
| spot checks (174 G>C AF 0.8x, 3037 F924, 11109 A3615V, 14408 P4715L, 18788 T6175I, 20209 SRNL6649I del, 22712 S P384S sb_fdr, 25563 ORF3a Q57H, 28214 ORF8 frameshift) | 9 | 9/9 |
| fastp HTML contains "duplication rate:" | yes | yes |

The same job through a GoWe 0.26.1 server and one Apptainer worker (throwaway instance, `gowe submit --no-upload`):
12 tasks SUCCESS in about 50 s, annotated VCF identical to the `cwl-runner` result apart from `##` header lines.

Quick re-check: `grep -c '^NC_045512.2' <name>.snpeff.vcf` and `cut -f7 <name>.snpeff.vcf | grep -v '^#' | sort | uniq -c`.

## Images

BioContainers images matching the Galaxy tool versions, pulled as `docker://` by Apptainer (no `--image-dir`
SIF needed). Digests at the time of the port:

| Image | Digest |
|---|---|
| quay.io/biocontainers/fastp:1.4.0--h43da1c4_0 | sha256:7dcc9e128315636a3c471a82617ca7a103e823cc22d65beb28794d06c1920e1d |
| quay.io/biocontainers/bwa:0.7.19--h577a1d6_1 | sha256:99a35e5ee4e9c329e8746c4689890b97a3ac5620cb36d374cba69ba52016e72a |
| quay.io/biocontainers/samtools:1.22--h96c455f_0 | sha256:6d00e632bae58b7ebbc1e4419194afe51ffdf9c56bc964df808db4f4388038d1 |
| quay.io/biocontainers/picard:3.1.1--hdfd78af_1 | sha256:c7add90b3ad28390af8f292946ec32d55074fbd661acdd4eb86cf2ac4cc99a31 |
| quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14 | sha256:421511647e6fbcc276fef55a11ffd1e9b9ec7796ac86c318559db242546442bc |
| quay.io/biocontainers/snpeff:4.5covid19--hdfd78af_2 | sha256:fc12b998af7683b5eebd9a454ddda5b280371f91901efa48ef717f285758ee37 |
| quay.io/biocontainers/multiqc:1.35--pyhdfd78af_2 | sha256:ceace1ca5329886d92600c225d34a523020c163b1599403e44c8d895d2dfb2a2 |

## Differences from the Galaxy workflow

- The bwa image has no samtools, so mapping writes SAM and `samtools-filter.cwl` does the coordinate sort that
  Galaxy's bwa_mem wrapper does internally, followed by the samtools_view filter.
- The reference is indexed inside each tool (bwa index, samtools faidx); cheap for a 30 kb genome.
- `lofreq call` runs with `--bonf dynamic`: the IWC value 0 means dynamic in the Galaxy wrapper.
- Trimmed reads, the unfiltered BAM and intermediate BAMs are not workflow outputs (Galaxy keeps them in the
  history); only the final indelqual BAM is.
- Inputs are a list of `{name, forward, reverse}` records instead of a Galaxy `list:paired` collection.
- `samtools` and `lofreq viterbi` run with `--no-PG`, so BAM `@PG` header lines differ from Galaxy's; reads and
  variant calls are unaffected.
