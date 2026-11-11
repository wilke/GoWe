cwlVersion: v1.2
class: Workflow
id: sars-cov-2-wgs-sample
label: SARS-CoV-2 WGS paired-end variant calling for one sample
doc: |
  Per-sample part of sars-cov-2-wgs-pe.cwl: fastp -> BWA-MEM ->
  sort + MAPQ/proper-pair filter -> Picard dedup -> LoFreq viterbi ->
  indelqual -> call -> strand-bias filter -> SnpEff (covid19 build).
  samtools stats runs on the filtered, pre-dedup BAM as in the IWC workflow.

requirements:
  InlineJavascriptRequirement: {}
  StepInputExpressionRequirement: {}

inputs:
  sample:
    type:
      type: record
      name: paired_sample
      fields:
        name: string
        forward: File
        reverse: File
  reference:
    type: File
  threads:
    type: int
    default: 2

outputs:
  fastp_html:
    type: File
    outputSource: fastp/report_html
  fastp_json:
    type: File
    outputSource: fastp/report_json
  samtools_stats:
    type: File
    outputSource: stats/stats
  markdup_metrics:
    type: File
    outputSource: markdup/metrics
  final_bam:
    type: File
    outputSource: indelqual/indelqual_bam
  called_variants:
    type: File
    outputSource: call/variants
  soft_filtered_variants:
    type: File
    outputSource: filter/filtered_vcf
  annotated_variants:
    type: File
    outputSource: snpeff/annotated_vcf
  snpeff_stats:
    type: File
    outputSource: snpeff/stats_html

steps:
  fastp:
    run: fastp.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      forward: {source: sample, valueFrom: $(self.forward)}
      reverse: {source: sample, valueFrom: $(self.reverse)}
      threads: threads
    out: [trimmed_forward, trimmed_reverse, report_json, report_html]

  map:
    run: bwa-mem.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      reference: reference
      forward: fastp/trimmed_forward
      reverse: fastp/trimmed_reverse
      threads: threads
    out: [sam]

  filter_reads:
    run: samtools-filter.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      alignments: map/sam
      threads: threads
    out: [bam]

  stats:
    run: samtools-stats.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      bam: filter_reads/bam
    out: [stats]

  markdup:
    run: picard-markdup.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      bam: filter_reads/bam
    out: [dedup_bam, metrics]

  realign:
    run: lofreq-viterbi.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      reference: reference
      bam: markdup/dedup_bam
      threads: threads
    out: [realigned_bam]

  indelqual:
    run: lofreq-indelqual.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      reference: reference
      bam: realign/realigned_bam
    out: [indelqual_bam]

  call:
    run: lofreq-call.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      reference: reference
      bam: indelqual/indelqual_bam
      threads: threads
    out: [variants]

  filter:
    run: lofreq-filter.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      vcf: call/variants
    out: [filtered_vcf]

  snpeff:
    run: snpeff-sars-cov-2.cwl
    in:
      sample_name: {source: sample, valueFrom: $(self.name)}
      vcf: filter/filtered_vcf
    out: [annotated_vcf, stats_html]
