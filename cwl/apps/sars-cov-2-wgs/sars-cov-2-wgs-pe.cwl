cwlVersion: v1.2
class: Workflow
id: sars-cov-2-wgs-pe
label: "COVID-19: variation analysis on WGS paired-end data"
doc: |
  CWL port of the Galaxy IWC workflow "COVID-19: variation analysis on WGS
  PE data" (release 0.2.7, MIT, author Wolfgang Maier;
  github.com/galaxyproject/iwc, workflows/sars-cov-2-variant-calling/
  sars-cov-2-pe-illumina-wgs-variant-calling). Paired-end Illumina reads
  are mapped with BWA-MEM to NC_045512.2 and variants are called with
  LoFreq across a wide range of allele frequencies, strand-bias flagged
  (not dropped) and annotated with SnpEff. Samples run in parallel; one
  MultiQC report covers all samples.

  The reference must be NC_045512.2: SnpEff uses the bundled NC_045512.2
  database and the chromosome names have to match.

requirements:
  ScatterFeatureRequirement: {}
  SubworkflowFeatureRequirement: {}
  InlineJavascriptRequirement: {}
  StepInputExpressionRequirement: {}
  MultipleInputFeatureRequirement: {}

inputs:
  samples:
    label: Paired-end samples (name, forward and reverse FASTQ)
    type:
      type: array
      items:
        type: record
        name: paired_sample
        fields:
          name: string
          forward: File
          reverse: File
  reference:
    label: NC_045512.2 FASTA sequence of SARS-CoV-2
    type: File
  threads:
    type: int
    default: 2

outputs:
  annotated_variants:
    type: File[]
    outputSource: per_sample/annotated_variants
  soft_filtered_variants:
    type: File[]
    outputSource: per_sample/soft_filtered_variants
  called_variants:
    type: File[]
    outputSource: per_sample/called_variants
  final_bams:
    type: File[]
    outputSource: per_sample/final_bam
  fastp_html_reports:
    type: File[]
    outputSource: per_sample/fastp_html
  snpeff_stats:
    type: File[]
    outputSource: per_sample/snpeff_stats
  preprocessing_and_mapping_report:
    type: File
    outputSource: multiqc/report_html

steps:
  per_sample:
    run: sars-cov-2-wgs-sample.cwl
    scatter: sample
    in:
      sample: samples
      reference: reference
      threads: threads
    out: [fastp_html, fastp_json, samtools_stats, markdup_metrics, final_bam,
          called_variants, soft_filtered_variants, annotated_variants, snpeff_stats]

  multiqc:
    run: multiqc.cwl
    in:
      reports:
        source: [per_sample/fastp_json, per_sample/samtools_stats, per_sample/markdup_metrics]
        linkMerge: merge_flattened
      report_name: {default: preprocessing_and_mapping_report}
    out: [report_html]
