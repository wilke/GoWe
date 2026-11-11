cwlVersion: v1.2
class: Workflow
id: viral-consensus-pe
label: Variant calling and consensus construction for non-segmented viral genomes (paired-end)
doc: |
  CWL port of the Galaxy IWC workflow "Variant calling and consensus
  construction from paired end short read data of non-segmented viral
  genomes" (release 0.4, AGPL-3.0-or-later; github.com/galaxyproject/iwc,
  workflows/virology/generic-non-segmented-viral-variant-calling).
  Original Galaxy workflow by Peter van Heusden and Wolfgang Maier (IWC);
  ported to CWL for GoWe, 2026-11.

  For batches of Illumina paired-end sequenced viruses with a simple,
  stable genome (e.g. morbilliviruses such as measles): reads are trimmed
  (fastp), mapped (BWA-MEM), filtered and realigned (LoFreq viterbi);
  with a primer scheme, primers are trimmed and reads outside amplicons
  dropped (iVar trim). iVar calls variants (annotated with a SnpEff
  database built from the reference GTF) and builds a consensus per
  sample. Outputs combine all samples: consensus FASTA, variant report and
  a MultiQC report.

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
    label: FASTA reference genome
    type: File
  annotation_gtf:
    label: Reference annotation (GTF, sequence names matching the FASTA)
    type: File
  primer_scheme:
    label: Primer scheme BED (optional; amplicon data only)
    type: File?
  min_freq:
    label: Supporting read fraction to call a variant (0.05-0.25); consensus uses 0.95 minus this
    type: float
    default: 0.25
  min_qual:
    label: Minimum base quality for variant calling and consensus (0-93)
    type: int
    default: 20
  threads:
    type: int
    default: 2

outputs:
  consensus_genomes:
    label: Combined consensus genomes for all samples
    type: File
    outputSource: combine/consensus_genomes
  variant_report:
    label: Combined variant report for all samples
    type: File
    outputSource: combine/variant_report
  quality_control_report:
    type: File
    outputSource: multiqc/report_html
  per_sample_consensus:
    type: File[]
    outputSource: per_sample/consensus
  annotated_variants:
    type: File[]
    outputSource: per_sample/variants_vcf
  processed_bams:
    type: File[]
    outputSource: per_sample/processed_bam
  fastp_html_reports:
    type: File[]
    outputSource: per_sample/fastp_html

steps:
  snpeff_db:
    run: snpeff-build.cwl
    in:
      reference: reference
      annotation_gtf: annotation_gtf
    out: [snpeff_db]

  primers:
    run: primer-scheme-prep.cwl
    when: $(inputs.primer_scheme !== null)
    in:
      primer_scheme: primer_scheme
    out: [ivar_bed, amplicon_info]

  per_sample:
    run: viral-consensus-sample.cwl
    scatter: sample
    in:
      sample: samples
      reference: reference
      snpeff_db: snpeff_db/snpeff_db
      ivar_bed: primers/ivar_bed
      amplicon_info: primers/amplicon_info
      min_freq: min_freq
      min_qual: min_qual
      threads: threads
    out: [fastp_html, fastp_json, samtools_stats, qualimap_dir, processed_bam,
          variants_vcf, snpeff_stats, variant_table, consensus]

  multiqc:
    run: multiqc.cwl
    in:
      reports:
        source: [per_sample/fastp_json, per_sample/samtools_stats]
        linkMerge: merge_flattened
      report_dirs: per_sample/qualimap_dir
    out: [report_html]

  combine:
    run: combine-results.cwl
    in:
      consensus: per_sample/consensus
      variant_tables: per_sample/variant_table
    out: [consensus_genomes, variant_report]
