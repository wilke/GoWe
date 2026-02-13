# CWL Tool Verification Report

Generated: 2026-02-13

## Summary

- **Total apps**: 39 (from BV-BRC `enumerate_apps` API)
- **Verified via Go SDK**: 19 apps (full parameter + group schema verification)
- **Verified via Perl source**: 6 apps (parameter use + output files)
- **API-only (unverified)**: ~14 apps (improved type mappings applied, no source verification)

## Verification Sources

| Source | Location | Apps Covered |
|--------|----------|-------------|
| Go SDK submit commands | `BV-BRC-Go-SDK/cmd/p3-submit-*/` | 19 apps |
| Perl service scripts | `bvbrc_standalone_apps/service-scripts/App-*.pl` | 5 apps |
| Perl service scripts | `dev_container/modules/sra_import/App-SRA.pl` | 1 app |
| BV-BRC API (`enumerate_apps`) | Live API | All 39 apps |

## Generator Improvements Applied

1. **Type mapping**: `list/array` → `string[]`, `wstype` → `File`, `folder` → `Directory`
2. **Group schemas**: `paired_end_libs`, `single_end_libs`, `paired_end_lib`, `single_end_lib`, `srr_libs`, `srr_ids`, `fasta_files`, `sequences` → CWL record array types
3. **Enum values**: Appended to doc field as `[enum: val1, val2, ...]`
4. **BV-BRC type annotations**: Original type in doc field as `[bvbrc:type]`
5. **Framework parameters**: `output_file` injected as optional when missing from spec

---

## SDK-Verified Apps (19)

### GenomeAssembly2

**SDK Command**: `p3-submit-genome-assembly`
**App ID**: `GenomeAssembly2`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| paired_end_libs | record[] | group | no | [] | {read1: File, read2: File?, platform: string?, interleaved: boolean, read_orientation_outward: boolean} |
| single_end_libs | record[] | group | no | [] | {read: File, platform: string?} |
| srr_ids | string[] | list | no | [] | SRA run accessions |
| recipe | string | enum | no | "auto" | auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell |
| trim_reads | boolean | bool | no | false | |
| racon_iter | int | int | no | 0 | Racon polishing iterations |
| pilon_iter | int | int | no | 0 | Pilon polishing iterations |
| min_contig_len | int | int | no | 300 | |
| min_contig_cov | int | int | no | 5 | |
| platform | string | enum | no | "infer" | infer, illumina, pacbio, nanopore, iontorrent |
| genome_size | string | string | no | "" | For canu recipe |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### ComprehensiveGenomeAnalysis

**SDK Command**: `p3-submit-CGA`
**App ID**: `ComprehensiveGenomeAnalysis`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| input_type | string | enum | yes | — | reads, contigs |
| taxonomy_id | int | int | yes | — | NCBI taxonomy ID |
| scientific_name | string | string | yes | — | |
| recipe | string | enum | no | "auto" | auto, full_spades, fast, miseq, smart, kiki |
| trim | boolean | bool | no | false | |
| code | int | enum | no | 11 | 4, 11 (genetic code) |
| domain | string | enum | no | "Bacteria" | Bacteria, Archaea |
| paired_end_libs | record[] | group | cond | [] | Same schema as GenomeAssembly2 |
| single_end_libs | record[] | group | cond | [] | |
| srr_ids | string[] | list | cond | [] | |
| contigs | File | wstype | cond | — | Required if input_type=contigs |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### Homology

**SDK Command**: `p3-submit-BLAST`
**App ID**: `Homology`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| input_type | string | enum | yes | — | dna, aa |
| db_type | string | enum | yes | — | fna, ffn, frn, faa |
| blast_program | string | enum | yes | — | blastn, blastx, tblastn, blastp (derived from input_type × db_type) |
| input_source | string | enum | yes | — | fasta_file, id_list |
| db_source | string | enum | yes | — | fasta_file, genome_list, taxon_list, precomputed_database |
| blast_evalue_cutoff | float | float | no | 1e-5 | |
| blast_max_hits | int | int | no | 10 | ≥1 |
| blast_min_coverage | int | int | no | 0 | 0–100 |
| input_fasta_file | File | wstype | cond | — | |
| input_id_list | string[] | list | cond | — | |
| db_fasta_file | File | wstype | cond | — | |
| db_genome_list | string[] | list | cond | — | |
| db_taxon_list | string[] | list | cond | — | |
| db_precomputed_database | string | enum | cond | — | BV-BRC, REFSEQ, Plasmids, Phages |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### RNASeq

**SDK Command**: `p3-submit-rnaseq`
**App ID**: `RNASeq`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| reference_genome_id | string | string | yes | — | MarkFlagRequired |
| recipe | string | enum | no | "RNA-Rocket" | RNA-Rocket, Host |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File, condition: int} — note extra `condition` field |
| single_end_libs | record[] | group | cond | [] | {read: File, condition: int} |
| srr_ids | record[] | group | cond | [] | {srr_accession: string, condition: int} |
| experimental_conditions | string[] | list | cond | [] | Condition names |
| contrasts | string | string | cond | [] | Condition index pairs |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

#### Notes
- Group schemas have extra `condition: int` field not in canonical schema
- Conditions are 1-indexed integers auto-assigned from read library flags

---

### Variation

**SDK Command**: `p3-submit-variation-analysis`
**App ID**: `Variation`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| reference_genome_id | string | string | yes | — | MarkFlagRequired |
| mapper | string | enum | no | "BWA-mem" | BWA-mem, BWA-mem-strict, Bowtie2, LAST, minimap2 |
| caller | string | enum | no | "FreeBayes" | FreeBayes, BCFtools |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File} — simpler schema (no platform) |
| single_end_libs | record[] | group | cond | [] | {read: File} |
| srr_ids | string[] | list | cond | [] | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### GenomeAnnotation

**SDK Command**: `p3-submit-genome-annotation`
**App ID**: `GenomeAnnotation` or `GenomeAnnotationGenbank`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| contigs | File | wstype | cond | — | Required for contigs mode |
| genbank_file | File | wstype | cond | — | Required for genbank mode |
| scientific_name | string | string | no | "Unknown sp." | |
| taxonomy_id | int | int | no | 6666666 | |
| code | int | enum | no | 11 | 4, 11 |
| domain | string | enum | no | "Bacteria" | Bacteria, Archaea, Viruses |
| recipe | string | string | no | — | "phage" for phage mode |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

#### Notes
- Two execution modes: contigs (GenomeAnnotation) vs genbank (GenomeAnnotationGenbank)
- Phage mode sets defaults: recipe="phage", domain="Viruses"

---

### CodonTree

**SDK Command**: `p3-submit-codon-tree`
**App ID**: `CodonTree`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| genome_ids | string[] | list | yes | — | Min 3 genomes required |
| number_of_genes | int | int | no | 100 | ≥10 |
| bootstraps | int | int | no | 100 | Hard-coded |
| max_genomes_missing | int | int | no | 0 | 0–10 |
| max_allowed_dups | int | int | no | 0 | 0–10 |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### MetagenomeBinning

**SDK Command**: `p3-submit-metagenome-binning`
**App ID**: `MetagenomeBinning`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| contigs | File | wstype | cond | — | |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File} |
| single_end_libs | record[] | group | cond | [] | {read: File} |
| srr_ids | string[] | list | cond | [] | |
| perform_bacterial_annotation | string | string | no | "true" | Boolean as string! |
| perform_viral_annotation | string | string | no | "true" | Boolean as string! |
| danglen | int | int | no | 50 | |
| genome_group | string | string | no | — | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

#### Notes
- Boolean params sent as strings "true"/"false" (not Go bool)
- assembler hard-coded to "auto"

---

### MSA

**SDK Command**: `p3-submit-MSA`
**App ID**: `MSA`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| alphabet | string | enum | yes | "dna" | dna, protein |
| aligner | string | enum | yes | "Muscle" | Muscle, Mafft |
| fasta_files | record[] | group | cond | — | {file: File, type: string} |
| feature_groups | string[] | list | cond | — | Workspace paths |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### SubspeciesClassification

**SDK Command**: `p3-submit-SubspeciesClassification`
**App ID**: `SubspeciesClassification`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| virus_type | string | enum | yes | "INFLUENZAH5" | 24 virus types (BOVDIARRHEA1, DENGUE, HCV, INFLUENZAH5, JAPANENCEPH, MASTADENO_A-F, MEASLES, MPOX, MUMPS, MURRAY, NOROORF1, NOROORF2, ROTAA, STLOUIS, SWINEH1, SWINEH1US, SWINEH3, TKBENCEPH, WESTNILE, YELLOWFEVER, ZIKA) |
| input_source | string | string | yes | "fasta_file" | Hard-coded |
| input_fasta_file | File | wstype | yes | — | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### ComparativeSystems

**SDK Command**: `p3-submit-comparative-systems`
**App ID**: `ComparativeSystems`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| genome_ids | string[] | list | cond | — | Comma-delimited or file |
| genome_groups | string[] | list | cond | — | Workspace paths |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### FastqUtils

**SDK Command**: `p3-submit-fastqutils`
**App ID**: `FastqUtils`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| recipe | string[] | list | yes | — | Built from boolean flags: paired_filter, trim, fastqc, align |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File} |
| single_end_libs | record[] | group | cond | [] | {read: File} |
| srr_ids | string[] | list | cond | — | |
| reference_genome_id | string | string | cond | — | Required for "align" recipe |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### GeneTree

**SDK Command**: `p3-submit-gene-tree`
**App ID**: `GeneTree`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| sequences | record[] | group | yes | — | {filename: File, type: string} |
| alphabet | string | enum | yes | — | DNA, Protein |
| recipe | string | enum | yes | "RAxML" | RAxML, PhyML, FastTree |
| trim_threshold | float | float | no | — | >0 to include |
| gap_threshold | float | float | no | — | >0 to include |
| substitution_model | string | enum | no | — | HKY85, JC69, K80, F81, F84, TN93, GTR (DNA); LG, WAG, JTT, etc. (Protein) |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### MetagenomicReadMapping

**SDK Command**: `p3-submit-metagenomic-read-mapping`
**App ID**: `MetagenomicReadMapping`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| gene_set_type | string | string | yes | "predefined_list" | Fixed |
| gene_set_name | string | enum | yes | "CARD" | CARD, VFDB |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File} |
| single_end_libs | record[] | group | cond | [] | {read: File} |
| srr_ids | string[] | list | cond | — | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### GenomeComparison

**SDK Command**: `p3-submit-proteome-comparison`
**App ID**: `GenomeComparison`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| genome_ids | string[] | list | cond | [] | |
| user_genomes | File[] | list | cond | [] | Protein FASTA files (wstype: feature_protein_fasta) |
| user_feature_groups | string[] | list | cond | [] | Workspace paths |
| reference_genome_index | int | int | yes | 1 | 1-based index |
| min_seq_cov | float | float | yes | 0.30 | 0–1 |
| min_ident | float | float | yes | 0.1 | 0–1 |
| min_positives | float | float | yes | 0.2 | 0–1 |
| max_e_val | float | float | yes | 1e-5 | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### ComprehensiveSARS2Analysis

**SDK Command**: `p3-submit-sars2-assembly`
**App ID**: `ComprehensiveSARS2Analysis` (NOT "SARS2Assembly")
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| recipe | string | enum | yes | "auto" | auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore |
| domain | string | string | yes | "Viruses" | Fixed |
| taxonomy_id | int | int | no | 2697049 | SARS-CoV-2 |
| scientific_name | string | string | no | "Severe acute respiratory syndrome coronavirus 2" | |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File, platform: string, interleaved: boolean, read_orientation_outward: boolean} |
| single_end_libs | record[] | group | cond | [] | {read: File, platform: string} |
| srr_ids | string[] | list | cond | [] | |
| primers | string | string | no | — | |
| primer_version | string | string | no | — | |
| min_depth | int | int | no | 0 | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### TaxonomicClassification

**SDK Command**: `p3-submit-taxonomic-classification`
**App ID**: `TaxonomicClassification`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| analysis_type | string | enum | no | "pathogen" | pathogen, microbiome, 16S |
| database | string | enum | no | "bvbrc" | WGS: bvbrc, standard; 16S: SILVA, Greengenes |
| confidence | string | enum | no | "0.1" | 0, 0.1–0.9, 1 |
| save_classified_sequences | boolean | bool | no | false | |
| save_unclassified_sequences | boolean | bool | no | false | |
| host_genome | string | enum | no | "no_host" | homo_sapiens, mus_musculus, etc. (11 values) |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File} |
| single_end_libs | record[] | group | cond | [] | {read: File} |
| srr_ids | string[] | list | cond | [] | |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

---

### ViralAssembly

**SDK Command**: `p3-submit-viral-assembly`
**App ID**: `ViralAssembly`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| recipe | string | enum | no | "auto" | auto, IRMA |
| module | string | string | yes | "FLU" | Hard-coded |
| paired_end_lib | record | group | cond | — | SINGULAR (not array): {read1: File, read2: File} |
| single_end_lib | record | group | cond | — | SINGULAR: {read: File} |
| srr_id | string | string | cond | — | Single SRR ID, mutually exclusive |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

#### Notes
- Uses **SINGULAR** parameter names: `paired_end_lib`, `single_end_lib`, `srr_id`
- Exactly ONE input source allowed (mutually exclusive)

---

### SARS2Wastewater

**SDK Command**: `p3-submit-wastewater-analysis`
**App ID**: `SARS2Wastewater`
**Status**: Verified

#### Inputs
| Parameter | CWL Type | BV-BRC Type | Required | Default | Notes |
|-----------|----------|-------------|----------|---------|-------|
| strategy | string | enum | no | "onecodex" | onecodex |
| primers | string | string | no | "ARTIC" | Also embedded in each group entry |
| paired_end_libs | record[] | group | cond | [] | {read1: File, read2: File, primers: string, primer_version: string, sample_date: string?} |
| single_end_libs | record[] | group | cond | [] | {read: File, primers: string, primer_version: string, sample_date: string?} |
| srr_ids | record[] | group | cond | [] | {srr_accession: string, primers: string, primer_version: string, sample_date: string?} |
| output_path | Directory | folder | yes | — | |
| output_file | string | string | yes | — | |

#### Notes
- Group entries have embedded metadata: `primers`, `primer_version`, `sample_date`
- Default primer_version: "V5.3.2"
- srr_ids is an array of objects (not simple strings)

---

## Perl-Verified Apps (6)

### Date

**Script**: `App-Date.pl`
**App ID**: `Date`
**Status**: Verified (inputs + outputs)

#### Inputs
- No parameters consumed by app code (framework-only: output_path, output_file)

#### Outputs
| File | Type | Pattern |
|------|------|---------|
| now | txt | `{result_folder}/now` |

---

### Sleep

**Script**: `App-Sleep.pl`
**App ID**: `Sleep`
**Status**: Verified (inputs + outputs)

#### Inputs
| Parameter | Type | Notes |
|-----------|------|-------|
| sleep_time | int | Duration in seconds; defaults to 60 |

#### Outputs
- None (uses `donot_create_result_folder`)

#### Opt-Out Flags
- `donot_create_result_folder`: yes

---

### GenomeComparison (Perl)

**Script**: `App-GenomeComparison.pl`
**App ID**: `GenomeComparison`
**Status**: Verified (inputs + outputs)

#### Inputs
| Parameter | Type | Notes |
|-----------|------|-------|
| genome_ids | string[] | Genome IDs for comparison |
| user_genomes | File[] | Workspace paths to protein FASTA files |
| user_feature_groups | string[] | Feature group workspace paths |
| reference_genome_index | int | 1-based index |
| min_seq_cov | float | |
| min_positives | int | |
| min_ident | float | |
| max_e_val | float | |

#### Outputs
| File | Type | Pattern |
|------|------|---------|
| genome_comparison.txt | genome_comparison_table | Tab-delimited comparison |
| genome_comparison.xls | xls | Excel workbook |
| genome_comparison.json | json | Structured JSON data |
| ref_genome.txt | txt | Circos reference |
| comp_genome_*.txt | txt | Circos tracks |
| karyotype.txt | txt | Circos karyotype |
| large.tiles.txt | txt | Circos tiles |
| legend.html | html | Legend |
| circos.svg | svg | Circular visualization |
| circos_final.html | html | Final HTML page |

---

### FluxBalanceAnalysis

**Script**: `App-FluxBalanceAnalysis.pl`
**App ID**: `FluxBalanceAnalysis`
**Status**: Partial (delegated to ProbModelSEEDHelper)

#### Notes
- All parameter handling delegated to `Bio::ModelSEED::ProbModelSEED::ProbModelSEEDHelper`
- Uses both `donot_create_result_folder` and `donot_create_job_result`

#### Opt-Out Flags
- `donot_create_result_folder`: yes
- `donot_create_job_result`: yes

---

### PhylogeneticTree

**Script**: `App-PhylogeneticTree.pl`
**App ID**: `PhylogeneticTree`
**Status**: Verified (inputs + outputs)

#### Inputs
| Parameter | Type | Notes |
|-----------|------|-------|
| in_genome_ids | string[] | Input genome IDs |
| out_genome_ids | string[] | Outgroup genome IDs |
| full_tree_method | enum | ml, parsimony_bl, ft/FastTree |
| refinement | boolean-like | yes/true/no/false |

#### Outputs
| File | Type | Pattern |
|------|------|---------|
| {name}.final.nwk | nwk | Final Newick tree |
| {name}_final_rooted.nwk | nwk | Rooted Newick tree |
| {name}.nwk | nwk | Initial tree |
| {name}.json | json | Tree data JSON |
| {name}_final_rooted.json | json | Rooted tree JSON |
| {name}.sup | nwk | Support values |
| {name}.html | html | Tree visualization |
| {name}.rooted.html | html | Rooted tree visualization |
| {name}.report.xml | xml | Analysis report |
| pepr.log* | txt | Execution logs |

---

### SRA Import

**Script**: `App-SRA.pl` (in `dev_container/modules/sra_import/`)
**App ID**: SRA (API name TBD)
**Status**: Partial (incomplete implementation noted)

#### Outputs
| File | Type | Pattern |
|------|------|---------|
| *.bam | bam | Alignment files |
| *.bam.bai | bam | BAM index |
| *.counts | txt | Count matrices |
| *.wig | wig | Genome browser tracks |
| *.txt | txt | Text outputs |

---

## API-Only Apps (~14)

These apps have improved type mappings from Phase 1 but lack source-level verification.

| App ID | Notes |
|--------|-------|
| DifferentialExpression | |
| FunctionalClassification | |
| GapfillModel | |
| GenomeAlignment | |
| GenomeAnnotationGenbank | May be same as GenomeAnnotation in genbank mode |
| GenomeAnnotationGenbankTest | Test variant |
| GenomeAssembly | v1 (GenomeAssembly2 is current) |
| HASubtypeNumberingConversion | |
| MetaCATS | |
| ModelReconstruction | |
| PrimerDesign | |
| RASTJob | |
| RunProbModelSEEDJob | |
| SequenceSubmission | |
| SyntenyGraph | |
| TnSeq | |

---

## Cross-App Patterns

### Common Group Schemas

**paired_end_libs** (canonical, ~10 apps):
```
{read1: File, read2: File?, platform: string?, interleaved: boolean, read_orientation_outward: boolean}
```

**single_end_libs** (canonical, ~10 apps):
```
{read: File, platform: string?}
```

**Variants:**
- Variation, MetagenomeBinning, FastqUtils: Simpler schema (no platform/interleaved)
- RNASeq: Adds `condition: int` field
- SARS2Wastewater: Adds `primers`, `primer_version`, `sample_date` fields
- ViralAssembly: Uses **SINGULAR** names (`paired_end_lib`, `single_end_lib`)

### App ID Corrections

| SDK Command | Expected App ID | Actual App ID |
|------------|----------------|---------------|
| p3-submit-sars2-assembly | SARS2Assembly | **ComprehensiveSARS2Analysis** |
| p3-submit-wastewater-analysis | WastewaterAnalysis | **SARS2Wastewater** |
| p3-submit-proteome-comparison | ProteomeComparison | **GenomeComparison** |

### File Type Conventions (wstype → CWL)

| wstype | CWL Type | Used For |
|--------|----------|----------|
| reads | File | Sequencing read files |
| contigs | File | FASTA contigs/assemblies |
| feature_dna_fasta | File | DNA FASTA sequences |
| feature_protein_fasta | File | Protein FASTA sequences |
| folder | Directory | Workspace folders |

### Framework Parameters

All apps have `output_path` (Directory) and `output_file` (string), injected by the framework if not declared in the app spec.
