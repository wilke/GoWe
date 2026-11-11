cwlVersion: v1.2
class: Workflow
id: viral-consensus-sample
label: Viral variant calling and consensus for one paired-end sample
doc: |
  Per-sample part of viral-consensus-pe.cwl: fastp -> BWA-MEM -> sort ->
  MAPQ/proper-pair filter -> LoFreq viterbi -> [iVar trim when a primer
  scheme is given] -> iVar variants (VCF, PASS only) -> SnpEff ->
  SnpSift table, and iVar consensus. samtools stats (sorted BAM) and
  QualiMap (filtered BAM) feed the MultiQC report. Characters other than
  letters, digits, _ and - in a sample name become _, as in Galaxy.

requirements:
  InlineJavascriptRequirement: {}
  StepInputExpressionRequirement: {}
  MultipleInputFeatureRequirement: {}

inputs:
  sample:
    type:
      type: record
      name: paired_sample
      fields:
        name: string
        forward: File
        reverse: File
  reference: File
  snpeff_db: Directory
  ivar_bed: File?
  amplicon_info: File?
  min_freq: float
  min_qual: int
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
  qualimap_dir:
    type: Directory
    outputSource: qualimap/report_dir
  processed_bam:
    type: File
    outputSource: [trim/trimmed_bam, realign/realigned_bam]
    pickValue: first_non_null
  variants_vcf:
    type: File
    outputSource: snpeff/annotated_vcf
  snpeff_stats:
    type: File
    outputSource: snpeff/stats_html
  variant_table:
    type: File
    outputSource: table/table
  consensus:
    type: File
    outputSource: consensus/consensus

steps:
  fastp:
    run: fastp.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      forward: {source: sample, valueFrom: $(self.forward)}
      reverse: {source: sample, valueFrom: $(self.reverse)}
      threads: threads
    out: [trimmed_forward, trimmed_reverse, report_json, report_html]

  map:
    run: bwa-mem.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      reference: reference
      forward: fastp/trimmed_forward
      reverse: fastp/trimmed_reverse
      threads: threads
    out: [sam]

  sort_filter:
    run: samtools-sort-filter.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      alignments: map/sam
      threads: threads
    out: [sorted_bam, filtered_bam]

  stats:
    run: samtools-stats.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      reference: reference
      bam: sort_filter/sorted_bam
    out: [stats]

  qualimap:
    run: qualimap-bamqc.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      bam: sort_filter/filtered_bam
      threads: threads
    out: [report_dir]

  realign:
    run: lofreq-viterbi.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      reference: reference
      bam: sort_filter/filtered_bam
      threads: threads
    out: [realigned_bam]

  trim:
    run: ivar-trim.cwl
    when: $(inputs.ivar_bed !== null)
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      bam: realign/realigned_bam
      ivar_bed: ivar_bed
      amplicon_info: amplicon_info
      threads: threads
    out: [trimmed_bam]

  variants:
    run: ivar-variants.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      reference: reference
      bam:
        source: [trim/trimmed_bam, realign/realigned_bam]
        pickValue: first_non_null
      min_qual: min_qual
      min_freq: min_freq
    out: [variants_tsv]

  to_vcf:
    run: ivar-variants-to-vcf.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      variants_tsv: variants/variants_tsv
    out: [vcf]

  snpeff:
    run: snpeff-eff.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      snpeff_db: snpeff_db
      vcf: to_vcf/vcf
    out: [annotated_vcf, stats_html]

  table:
    run: snpsift-extract.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      vcf: snpeff/annotated_vcf
    out: [table]

  consensus:
    run: ivar-consensus.cwl
    in:
      sample_name: {source: sample, valueFrom: '$(self.name.replace(/[^\w-]/g, "_"))'}
      bam:
        source: [trim/trimmed_bam, realign/realigned_bam]
        pickValue: first_non_null
      min_qual: min_qual
      min_freq:
        source: min_freq
        valueFrom: $(Math.round((0.95 - self) * 1e6) / 1e6)
    out: [consensus]
