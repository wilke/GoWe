# Viral variant calling and consensus, non-segmented genomes (IWC port)

CWL port of the Galaxy IWC workflow **"Variant calling and consensus construction from paired end short read
data of non-segmented viral genomes"**, release 0.4 (AGPL-3.0-or-later;
[iwc/workflows/virology/generic-non-segmented-viral-variant-calling](https://github.com/galaxyproject/iwc/tree/main/workflows/virology/generic-non-segmented-viral-variant-calling)).
For batches of Illumina paired-end sequenced viruses with a simple, stable genome (e.g. morbilliviruses such as
measles); works for any such virus given a reference FASTA and a GTF annotation, with or without a primer scheme.

`viral-consensus-pe.cwl` builds a SnpEff database from the reference once, prepares the primer scheme once (if
given), runs `viral-consensus-sample.cwl` on every sample in parallel and combines the results:

```
fastp -> bwa mem (RG) -> samtools sort -> view (-q 20 -f 3) -> lofreq viterbi
      -> [ivar trim, only with a primer scheme] -> ivar variants -> VCF (PASS) -> snpEff (viralDB) -> SnpSift table
                                               -> ivar consensus (t = 0.95 - min_freq, depth 20, N below)
samtools stats (sorted BAM) + QualiMap (filtered BAM) + fastp -> MultiQC
```

Each tool's `doc` names the Galaxy tool version and the parameters it reproduces; flags come from the IWC `.ga` file
and the Galaxy wrappers in tools-iuc / tools-devteam.

## License

This app is a derivative of an AGPL-3.0-or-later workflow and is distributed under the same license: see
[`LICENSE`](LICENSE) in this folder. It applies to the files in `cwl/apps/viral-consensus/` only, not to the rest of
GoWe.

Original Galaxy workflow by Peter van Heusden (ORCID 0000-0001-6553-5274) and Wolfgang Maier
(ORCID 0000-0002-9464-6640), Galaxy IWC; ported to CWL for GoWe in November 2026 (modified: see the differences below).

The embedded `ivar_variants_to_vcf.py` comes unchanged from galaxyproject/tools-iuc and is under its MIT license:

```
The MIT License (MIT)

Copyright (c) 2014 galaxy-iuc

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Run

```bash
gowe submit viral-consensus-pe.cwl -i job.yml                                   # on GoWe workers
cwl-runner --runtime apptainer --parallel viral-consensus-pe.cwl job.yml        # or locally
```

Inputs (see `job.example.yml`): `samples` (list of `{name, forward, reverse}`), `reference` (FASTA),
`annotation_gtf` (GTF with sequence names matching the FASTA), optional `primer_scheme` (BED; amplicon data only),
`min_freq` (supporting read fraction to call a variant, 0.05-0.25, default 0.25), `min_qual` (default 20),
`threads` (default 2).

Outputs: `consensus_genomes.fa` (one record per sample, header = sample name), `variant_report.tsv` (all samples,
first column `Sample`), `quality_control_report.html` (MultiQC), and per sample the consensus FASTA, the
SnpEff-annotated VCF, the final BAM and the fastp report.

## Validation against the IWC tests

Test data: `measles.fa`, `measles.gtf`, `measles_primer_scheme.bed` from the IWC `test-data/`, reads
`SRR30155677_region_filtered_R{1,2}.fq.gz` and `SRR30155702_subsampled_R{1,2}.fq.gz` from
https://zenodo.org/records/16495208. Both test cases of `pe-illumina-simple-virus-calling-and-consensus-test.yml`,
run with `cwl-runner` 0.26.1 (Apptainer) on 2026-11-10:

| Assertion | WGS mode (no primers) | Amplicon mode (primer scheme) |
|---|---|---|
| one consensus record per sample (`>SRR30155677_region_filtered`, `>SRR30155702_subsampled`) | yes | yes |
| expected consensus fragments (Ns outside the filtered region; IUPAC codes for the mixed sample) | both present | both present |
| fragment that must disappear in amplicon mode | n/a | absent |
| variant report header `Sample CHROM POS ... ANN[*].HGVS_P` | yes | yes |
| rows SRR30155677 / SRR30155702 (IWC: 43 / 599 and 39 / 498) | 43 / 599 | 39 / 498 |
| MultiQC sections fastp, Samtools, QualiMap | present | present |

The amplicon case also ran through a GoWe 0.26.1 server with one Apptainer worker (throwaway instance,
`gowe submit --no-upload`): 30 tasks SUCCESS in about 70 s; consensus FASTA and variant report byte-identical to
the `cwl-runner` result.

## Images

BioContainers images matching the Galaxy tool versions, pulled as `docker://`. Digests at the time of the port:

| Image | Digest |
|---|---|
| quay.io/biocontainers/fastp:1.4.0--h43da1c4_0 | sha256:7dcc9e128315636a3c471a82617ca7a103e823cc22d65beb28794d06c1920e1d |
| quay.io/biocontainers/bwa:0.7.19--h577a1d6_1 | sha256:99a35e5ee4e9c329e8746c4689890b97a3ac5620cb36d374cba69ba52016e72a |
| quay.io/biocontainers/samtools:1.22--h96c455f_0 | sha256:6d00e632bae58b7ebbc1e4419194afe51ffdf9c56bc964df808db4f4388038d1 |
| quay.io/biocontainers/lofreq:2.1.5--py39hc2527bd_14 | sha256:421511647e6fbcc276fef55a11ffd1e9b9ec7796ac86c318559db242546442bc |
| quay.io/biocontainers/qualimap:2.3--hdfd78af_0 | sha256:49d81e27bf995d0ef72ae46c79c22c8c779e21573cd76f7b60cf3f73af61b087 |
| quay.io/biocontainers/viramp-hub:0.1.0--pyhdfd78af_0 | sha256:fb5822e701bcc4228f1ac7077be76ed1fd58085544e8bf1152d7ecfe92835980 |
| quay.io/biocontainers/ivar:1.4.4--h077b44d_0 | sha256:1de5c4e08c00c037797137b903f0849b3a4d55d750a6e3a4efeda0fcfe795cb7 |
| quay.io/biocontainers/python:3.12 | sha256:29fccb317d61a9d09ecf432f63bfea512b98f0f3bc3764dc65fbdbc0f02473b5 |
| quay.io/biocontainers/snpeff:5.4.0c--hdfd78af_0 | sha256:832e86061ab3c1ef4339ec42f10fb98c4eaaecd498cd37d2bbe747e6bcc14de7 |
| quay.io/biocontainers/snpsift:5.4.0c--hdfd78af_0 | sha256:5683a819f59a36fec2eb0e9f6ca78a98494b200eb73a54dc6a1f6dcffb206e26 |
| quay.io/biocontainers/multiqc:1.35--pyhdfd78af_2 | sha256:ceace1ca5329886d92600c225d34a523020c163b1599403e44c8d895d2dfb2a2 |

## Differences from the Galaxy workflow

- 23 Galaxy tool steps become 16 CWL tools: parameter mapping, pick-value, collection flattening and the sed/cat/collapse
  text steps are CWL features (`when`, `pickValue`, `valueFrom`) or folded into neighbouring tools.
- The optional primer scheme maps to a CWL optional input; `ivar trim` runs only `when` it is given, and
  `pickValue: first_non_null` chooses the trimmed or the realigned BAM.
- The ivar TSV-to-VCF converter is the Galaxy wrapper's `ivar_variants_to_vcf.py` (tools-iuc, MIT), embedded
  unchanged in `ivar-variants-to-vcf.cwl`.
- QualiMap is a required step; the IWC workflow tolerates a failed QualiMap run ("Filter failed datasets").
- The bwa image has no samtools, so mapping writes SAM and `samtools-sort-filter.cwl` does the sort.
- Sample names are sanitized like Galaxy does (characters other than letters, digits, `_` and `-` become `_`).
