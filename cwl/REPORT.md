# BV-BRC CWL Tools Report

Generated: 2026-02-12

## Summary

- Total apps: 39
- Tools generated: 39

---

## CodonTree

**Label**: Compute phylogenetic tree from PGFam protein and DNA sequence
**Description**: Computes a phylogenetic tree based on protein and DNA sequences of PGFams for a set of genomes
**File**: `tools/CodonTree.cwl`

### Inputs (8 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_path | Directory | folder | yes | — | Path to which the output will be written  |
| output_file | string | wsid | yes | — | Basename for the generated output files |
| genome_ids | string | list | yes | — | Main genomes |
| optional_genome_ids | string? | list | no | — | Optional genomes (not penalized for missing/duplicated genes) |
| number_of_genes | int? | int | no | "20" | Desired number of genes |
| bootstraps | int? | int | no | "100" | Number of bootstrap replicates |
| max_genomes_missing | int? | int | no | "0" | Number of main genomes allowed missing from any PGFam |
| max_allowed_dups | int? | int | no | "0" | Number of duplications allowed for main genomes in any PGFam |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## ComparativeSystems

**Label**: Comparative Systems
**Description**: Create datastructures to decompose genomes
**File**: `tools/ComparativeSystems.cwl`

### Inputs (4 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| genome_ids | string? | list | no | — | Genome Ids |
| genome_groups | string? | list | no | — | Genome Groups |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## ComprehensiveGenomeAnalysis

**Label**: Comprehensive Genome Analysis
**Description**: Analyze a genome from reads or contigs, generating a detailed analysis report.
**File**: `tools/ComprehensiveGenomeAnalysis.cwl`

### Inputs (29 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_type | string | enum | yes | — | Input type (reads / contigs / genbank) [enum: reads, contigs, genbank] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| reference_assembly | string? | wstype | no | — | Reference set of assembled DNA contigs |
| gto | string? | wstype | no | — | Preannotated genome object |
| recipe | string? | enum | no | "auto" | Recipe used for assembly [enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell] |
| racon_iter | int? | int | no | 2 | Racon polishing iterations (for long reads) |
| pilon_iter | int? | int | no | 2 | Pilon polishing iterations (for short reads) |
| trim | boolean? | boolean | no | false | Trim reads before assembly |
| min_contig_len | int? | int | no | 300 | Filter out short contigs in final assembly |
| min_contig_cov | float? | float | no | 5 | Filter out contigs with low read depth in final assembly |
| genome_size | string? | string | no | "5M" | Estimated genome size (for canu) |
| genbank_file | string? | wstype | no | — | Genome to process |
| contigs | string? | wstype | no | — | Input set of DNA contigs for annotation |
| scientific_name | string | string | yes | — | Scientific name of genome to be annotated |
| taxonomy_id | int? | int | no | — | NCBI Taxonomy identfier for this genome |
| code | string | enum | yes | 11 | Genetic code used in translation of DNA sequences [enum: 11, 4] |
| domain | string | enum | yes | "Bacteria" | Domain of the submitted genome [enum: Bacteria, Archaea] |
| public | boolean? | bool | no | false | Make this genome public |
| queue_nowait | boolean? | bool | no | false | If set, don't wait for the indexing to finish before marking the job complete. |
| skip_indexing | boolean? | bool | no | false | If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| _parent_job | string? | string | no | — | (Internal) Parent job for this annotation |
| workflow | string? | string | no | — | Specifies a custom workflow document (expert). |
| analyze_quality | boolean? | bool | no | — | If enabled, run quality analysis on genome |
| debug_level | int? | int | no | 0 | Debugging level. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## ComprehensiveSARS2Analysis

**Label**: Comprehensive SARS2 Analysis
**Description**: Analyze a genome from reads or contigs, generating a detailed analysis report.
**File**: `tools/ComprehensiveSARS2Analysis.cwl`

### Inputs (25 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_type | string | enum | yes | — | Input type (reads / contigs / genbank) [enum: reads, contigs, genbank] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| recipe | string? | enum | no | "auto" | Recipe used for assembly [enum: auto, cdc-illumina, cdc-nanopore, artic-nanopore] |
| min_depth | int? | int | no | 100 | Minimum coverage to add reads to consensus sequence |
| keep_intermediates | int? | int | no | 0 | Keep all intermediate output from the pipeline |
| genbank_file | string? | wstype | no | — | Genome to process |
| contigs | string? | wstype | no | — | Input set of DNA contigs for annotation |
| scientific_name | string | string | yes | — | Scientific name of genome to be annotated |
| taxonomy_id | int | int | yes | — | NCBI Taxonomy identfier for this genome |
| code | string | enum | yes | 1 | Genetic code used in translation of DNA sequences |
| domain | string | enum | yes | "Viruses" | Domain of the submitted genome [enum: Bacteria, Archaea, Viruses] |
| public | boolean? | bool | no | false | Make this genome public |
| queue_nowait | boolean? | bool | no | false | If set, don't wait for the indexing to finish before marking the job complete. |
| skip_indexing | boolean? | bool | no | false | If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| reference_virus_name | string? | string | no | — | Reference virus name |
| container_id | string? | string | no | — | (Internal) Container to use for this run |
| _parent_job | string? | string | no | — | (Internal) Parent job for this annotation |
| workflow | string? | string | no | — | Specifies a custom workflow document (expert). |
| analyze_quality | boolean? | bool | no | — | If enabled, run quality analysis on genome |
| debug_level | int? | int | no | 0 | Debugging level. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## Date

**Label**: Date
**Description**: Returns the current date and time.
**File**: `tools/Date.cwl`

### Inputs (2 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## DifferentialExpression

**Label**: Transform expression data
**Description**: Parses and transforms users differential expression data
**File**: `tools/DifferentialExpression.cwl`

### Inputs (5 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| xfile | string | wstype | yes | — | Comparison values between samples |
| mfile | string? | wstype | no | — | Metadata template filled out by the user |
| ustring | string | string | yes | — | User information (JSON string) |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## FastqUtils

**Label**: Fastq Utilites
**Description**: Useful common processing of fastq files
**File**: `tools/FastqUtils.cwl`

### Inputs (7 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| reference_genome_id | string? | string | no | — | Reference genome ID |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_libs | string? | group | no | — |  |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| recipe | string | list | yes | — | Recipe |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## FluxBalanceAnalysis

**Label**: Run flux balance analysis
**Description**: Run flux balance analysis on model.
**File**: `tools/FluxBalanceAnalysis.cwl`

### Inputs (17 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| model | string | wstype | yes | — | Model on which to run flux balance analysis |
| media | string? | wstype | no | — | Media formulation for flux balance analysis |
| fva | boolean? | bool | no | false | Minimize and maximize each reaction to permit classificaton of reaction activity |
| predict_essentiality | boolean? | bool | no | false | Simulate the knockout of each gene in the model to evaluate gene essentiality |
| minimizeflux | boolean? | bool | no | false | Minimize sum of all fluxes in reported optimal solution |
| findminmedia | boolean? | bool | no | false | Predict the minimal media formulation that will support growth of current model |
| allreversible | boolean? | bool | no | false | Ignore existing reaction reversibilities and make all reactions reversible |
| thermo_const_type | string? | enum | no | — | Type of thermodynamic constraints [enum: None, Simple] |
| media_supplement | string? | string | no | — | Additional compounds to supplement media in FBA simulaton |
| geneko | string? | string | no | — | List of gene knockouts to simulation in FBA. |
| rxnko | string? | string | no | — | List of reaction knockouts to simulation in FBA. |
| objective_fraction | float? | float | no | 1 | Objective fraction for follow up analysis (e.g. FVA, essentiality prediction) |
| uptake_limit | string? | group | no | — |  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| custom_bounds | string? | group | no | — |  |
| objective | string? | group | no | — |  |
| output_path | Directory? | folder | no | — | Workspace folder for results (framework parameter) |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## FunctionalClassification

**Label**: classify reads
**Description**: Compute functional classification for read data
**File**: `tools/FunctionalClassification.cwl`

### Inputs (5 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GapfillModel

**Label**: Gapfill metabolic model
**Description**: Run gapfilling on model.
**File**: `tools/GapfillModel.cwl`

### Inputs (20 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| model | string | wstype | yes | — | Model on which to run flux balance analysis |
| media | string? | wstype | no | — | Media formulation for flux balance analysis |
| probanno | string? | wstype | no | — | Computed alternative potential annotations for genes to use in gapfilling functions |
| alpha | float? | float | no | 0 | Increase alpha to increase piority for comprehensive gapfilling |
| allreversible | boolean? | bool | no | false | Ignore existing reaction reversibilities and make all reactions reversible |
| allowunbalanced | boolean? | bool | no | false | Allow unbalanced reactions in gapfilling |
| integrate_solution | boolean? | bool | no | false | Integrate first gapfilling solution |
| thermo_const_type | string? | enum | no | — | Type of thermodynamic constraints [enum: None, Simple] |
| media_supplement | string? | string | no | — | Additional compounds to supplement media in FBA simulaton |
| geneko | string? | string | no | — | List of gene knockouts to simulation in FBA. |
| rxnko | string? | string | no | — | List of reaction knockouts to simulation in FBA. |
| target_reactions | string? | string | no | — | List of reactions that should be targets for gapfilling |
| objective_fraction | float? | float | no | 0.001 | Objective fraction for follow up analysis (e.g. FVA, essentiality prediction) |
| low_expression_theshold | float? | float | no | 1 | Threshold of expression for gene to be consider inactive |
| high_expression_theshold | float? | float | no | 1 | Threshold of expression for gene to be consider active |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| uptake_limit | string? | group | no | — |  |
| custom_bounds | string? | group | no | — |  |
| objective | string? | group | no | — |  |
| output_path | Directory? | folder | no | — | Workspace folder for results (framework parameter) |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GeneTree

**Label**: Gene Tree
**Description**: Estimate phylogeny of gene or other sequence feature
**File**: `tools/GeneTree.cwl`

### Inputs (13 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| sequences | string | — | yes | — | Sequence Data Inputs |
| alignment_program | string? | — | no | — | Alignment Program |
| trim_threshold | float? | float | no | — | Alignment End-Trimming Threshold |
| gap_threshold | float? | float | no | — | Delete Gappy Sequences Threshold |
| alphabet | string | enum | yes | — | Sequence alphabet: DNA or RNA or Protein [enum: DNA, Protein] |
| substitution_model | string? | enum | no | — | Substitution Model [enum: HKY85, JC69, K80, F81, F84, TN93, GTR, LG, WAG, JTT, MtREV, Dayhoff, DCMut, RtREV, CpREV, VT, AB, Blosum62, MtMam, MtArt, HIVw, HIVb] |
| bootstrap | int? | integer | no | — | Perform boostrapping |
| recipe | string? | enum | no | "RAxML" | Recipe used for FeatureTree analysis [enum: RAxML, PhyML, FastTree] |
| tree_type | string? | enum | no | — | Fields to be retrieved for each gene. [enum: viral_genome, gene] |
| feature_metadata_fields | string? | string | no | — | Fields to be retrieved for each gene. |
| genome_metadata_fields | string? | string | no | — | Fields to be retrieved for each genome. |
| output_path | Directory | folder | yes | — | Path to which the output will be written. |
| output_file | string | wsid | yes | — | Basename for the generated output files. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAlignment

**Label**: Multiple Whole Genome Alignment
**Description**: Uses Mauve to perform multiple whole genome alignment with rearrangements.
**File**: `tools/GenomeAlignment.cwl`

### Inputs (12 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genome_ids | string | list | yes | — | Genome IDs to Align |
| recipe | string? | enum | no | "progressiveMauve" | Mauve method to be used [enum: progressiveMauve, mauveAligner] |
| seedWeight | float? | float | no | — | Seed weight for calculating initial anchors. |
| maxGappedAlignerLength | float? | float | no | — | Maximum number of base pairs to attempt aligning with the gapped aligner. |
| maxBreakpointDistanceScale | float? | float | no | — | Set the maximum weight scaling by breakpoint distance.  Must be in [0, 1]. Defaults to 0.9. |
| conservationDistanceScale | float? | float | no | — | Scale conservation distances by this amount.  Must be in [0, 1].  Defaults to 1. |
| weight | float? | float | no | — | Minimum pairwise LCB score. |
| minScaledPenalty | float? | float | no | — | Minimum breakpoint penalty after scaling the penalty by expected divergence. |
| hmmPGoHomologous | float? | float | no | — | Probability of transitioning from the unrelated to the homologous state |
| hmmPGoUnrelated | float? | float | no | — | Probability of transitioning from the homologous to the unrelated state |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data. |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAnnotation

**Label**: Annotate genome
**Description**: Calls genes and functionally annotate input contig set.
**File**: `tools/GenomeAnnotation.cwl`

### Inputs (25 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| contigs | string | wstype | yes | — | Input set of DNA contigs for annotation |
| scientific_name | string | string | yes | — | Scientific name of genome to be annotated |
| taxonomy_id | int? | int | no | — | NCBI Taxonomy identfier for this genome |
| code | string | enum | yes | 11 | Genetic code used in translation of DNA sequences [enum: 11, 4] |
| domain | string | enum | yes | "Bacteria" | Domain of the submitted genome [enum: Bacteria, Archaea] |
| public | boolean? | bool | no | false | Make this genome public |
| queue_nowait | boolean? | bool | no | false | If set, don't wait for the indexing to finish before marking the job complete. |
| skip_indexing | boolean? | bool | no | false | If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. |
| skip_workspace_output | boolean? | bool | no | false | If set, don't write anything to workspace. |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| reference_virus_name | string? | string | no | — | Reference virus name |
| container_id | string? | string | no | — | (Internal) Container to use for this run |
| indexing_url | string? | string | no | — | (Internal) Override Data API URL for use in indexing |
| _parent_job | string? | string | no | — | (Internal) Parent job for this annotation |
| fix_errors | boolean? | bool | no | — | The automatic annotation process may run into problems, such as gene candidates overlapping RNAs, or genes embedded inside other genes. To automatically resolve these problems (even if that requires deleting some gene candidates), enable this option. |
| fix_frameshifts | boolean? | bool | no | — | If you wish for the pipeline to fix frameshifts, enable this option. Otherwise frameshifts will not be corrected. |
| enable_debug | boolean? | bool | no | — | If you wish debug statements to be printed for this job, enable this option. |
| verbose_level | int? | int | no | — | Set this to the verbosity level of choice for error messages. |
| workflow | string? | string | no | — | Specifies a custom workflow document (expert). |
| recipe | string? | string | no | — | Specifies a non-default annotation recipe |
| disable_replication | boolean? | bool | no | — | Even if this job is identical to a previous job, run it from scratch. |
| analyze_quality | boolean? | bool | no | — | If enabled, run quality analysis on genome |
| custom_pipeline | string? | group | no | — | Customize the RASTtk pipeline |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAnnotationGenbank

**Label**: Annotate genome
**Description**: Calls genes and functionally annotate input contig set.
**File**: `tools/GenomeAnnotationGenbank.cwl`

### Inputs (23 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genbank_file | string | wstype | yes | — | Genome to process |
| public | boolean? | bool | no | false | Make this genome public |
| queue_nowait | boolean? | bool | no | false | If set, don't wait for the indexing to finish before marking the job complete. |
| skip_indexing | boolean? | bool | no | false | If set, don't index this genome in solr. It will not be available for analysis through the PATRIC site. |
| skip_workspace_output | boolean? | bool | no | false | If set, don't write anything to workspace. |
| container_id | string? | string | no | — | (Internal) Container to use for this run |
| indexing_url | string? | string | no | — | (Internal) Override Data API URL for use in indexing |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| reference_virus_name | string? | string | no | — | Reference virus name |
| workflow | string? | string | no | — | Specifies a custom workflow document (expert). |
| recipe | string? | string | no | — | Specifies a non-default annotation recipe |
| scientific_name | string? | string | no | — | Scientific name of genome to be annotated. Overrides value in genbank file. |
| taxonomy_id | int? | int | no | — | NCBI Taxonomy identfier for this genome. Overrides value in genbank file. |
| code | string? | enum | no | — | Genetic code used in translation of DNA sequences. Overrides value in genbank file. [enum: 11, 4, 25] |
| domain | string? | enum | no | "Bacteria" | Domain of the submitted genome. Overrides value in genbank file. [enum: Bacteria, Archaea] |
| import_only | boolean? | bool | no | — | Import this genome (do not reannotate gene calls or protein functions) |
| fix_errors | boolean? | bool | no | — | The automatic annotation process may run into problems, such as gene candidates overlapping RNAs, or genes embedded inside other genes. To automatically resolve these problems (even if that requires deleting some gene candidates), enable this option. |
| fix_frameshifts | boolean? | bool | no | — | If you wish for the pipeline to fix frameshifts, enable this option. Otherwise frameshifts will not be corrected. |
| enable_debug | boolean? | bool | no | — | If you wish debug statements to be printed for this job, enable this option. |
| verbose_level | int? | int | no | — | Set this to the verbosity level of choice for error messages. |
| disable_replication | boolean? | bool | no | — | Even if this job is identical to a previous job, run it from scratch. |
| custom_pipeline | string? | group | no | — | Customize the RASTtk pipeline |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAnnotationGenbankTest

**Label**: Annotate genome
**Description**: Calls genes and functionally annotate input contig set.
**File**: `tools/GenomeAnnotationGenbankTest.cwl`

### Inputs (11 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genbank_file | string | wstype | yes | — | Genome to process |
| public | boolean? | bool | no | false | Make this genome public |
| queue_nowait | boolean? | bool | no | false | If set, don't wait for the indexing to finish before marking the job complete. |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| fix_errors | boolean? | bool | no | — | The automatic annotation process may run into problems, such as gene candidates overlapping RNAs, or genes embedded inside other genes. To automatically resolve these problems (even if that requires deleting some gene candidates), enable this option. |
| fix_frameshifts | boolean? | bool | no | — | If you wish for the pipeline to fix frameshifts, enable this option. Otherwise frameshifts will not be corrected. |
| enable_debug | boolean? | bool | no | — | If you wish debug statements to be printed for this job, enable this option. |
| verbose_level | int? | int | no | — | Set this to the verbosity level of choice for error messages. |
| disable_replication | boolean? | bool | no | — | Even if this job is identical to a previous job, run it from scratch. |
| custom_pipeline | string? | group | no | — | Customize the RASTtk pipeline |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAssembly

**Label**: Assemble reads
**Description**: Assemble reads into a set of contigs
**File**: `tools/GenomeAssembly.cwl`

### Inputs (10 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| reference_assembly | string? | wstype | no | — | Reference set of assembled DNA contigs |
| recipe | string? | enum | no | "auto" | Recipe used for assembly [enum: auto, full_spades, fast, miseq, smart, kiki] |
| pipeline | string? | string | no | — | Advanced assembly pipeline arguments that overrides recipe |
| min_contig_len | int? | int | no | 300 | Filter out short contigs in final assembly |
| min_contig_cov | float? | float | no | 5 | Filter out contigs with low read depth in final assembly |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeAssembly2

**Label**: Assemble WGS reads
**Description**: Assemble reads into a set of contigs
**File**: `tools/GenomeAssembly2.cwl`

### Inputs (13 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| recipe | string? | enum | no | "auto" | Recipe used for assembly [enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell] |
| racon_iter | int? | int | no | 2 | Racon polishing iterations (for long reads) |
| pilon_iter | int? | int | no | 2 | Pilon polishing iterations (for short reads) |
| trim | boolean? | boolean | no | false | Trim reads before assembly |
| min_contig_len | int? | int | no | 300 | Filter out short contigs in final assembly |
| min_contig_cov | float? | float | no | 5 | Filter out contigs with low read depth in final assembly |
| genome_size | string? | string | no | "5M" | Estimated genome size (for canu) |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| debug_level | int? | int | no | 0 | Debugging level. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## GenomeComparison

**Label**: Blast-based genome proteome comparison
**Description**: Compare the proteome sets from multiple genomes using Blast
**File**: `tools/GenomeComparison.cwl`

### Inputs (10 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genome_ids | string? | string | no | — | Genome IDs |
| user_genomes | string? | wstype | no | — | Genome protein sequence files in FASTA |
| user_feature_groups | string? | wstype | no | — | User feature groups |
| reference_genome_index | int? | int | no | 1 | Index of genome to be used as reference (1-based) |
| min_seq_cov | float? | float | no | 0.3 | Minimum coverage of query and subject |
| max_e_val | float? | float | no | 1e-05 | Maximum E-value |
| min_ident | float? | float | no | 0.1 | Minimum fraction identity |
| min_positives | float? | float | no | 0.2 | Minimum fraction positive-scoring positions |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## HASubtypeNumberingConversion

**Label**: HA Subtype Numbering Conversion
**Description**: HA Subtype Numbering Conversion
**File**: `tools/HASubtypeNumberingConversion.cwl`

### Inputs (7 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_source | string | enum | yes | — | Source of input (id_list, fasta_data, fasta_file, genome_group) [enum: fasta_data, fasta_file, feature_group] |
| input_fasta_data | string? | string | no | — | Input sequence in fasta formats |
| input_fasta_file | string? | wsid | no | — | Input sequence as a workspace file of fasta data |
| input_feature_group | string? | wsid | no | — | Input sequence as a workspace feature group |
| types | string | string | yes | — | Selected types in the submission |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## Homology

**Label**: Perform homology searches
**Description**: Perform homology searches on sequence data
**File**: `tools/Homology.cwl`

### Inputs (15 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_type | string | enum | yes | — | Type of input (dna or aa) [enum: dna, aa] |
| input_source | string | enum | yes | — | Source of input (id_list, fasta_data, fasta_file) [enum: id_list, fasta_data, fasta_file] |
| input_fasta_data | string? | string | no | — | Input sequence in fasta formats |
| input_id_list | string? | array | no | — | Input sequence as a list of sequence identifiers |
| input_fasta_file | string? | wsid | no | — | Input sequence as a workspace file of fasta data |
| db_type | string | enum | yes | — | Database type to search (protein / DNA / RNA / contigs) [enum: faa, ffn, frn, fna] |
| db_source | string | enum | yes | — | Source of database (fasta_data, fasta_file, genome_list, taxon_list, precomputed_database) [enum: fasta_data, fasta_file, genome_list, taxon_list, precomputed_database] |
| db_fasta_data | string? | string | no | — | Database sequences as fasta |
| db_fasta_file | string? | wsid | no | — | Database fasta file |
| db_genome_list | string? | array | no | — | Database genome list |
| db_taxon_list | string? | array | no | — | Database taxon list |
| db_precomputed_database | string? | string | no | — | Precomputed database name |
| blast_program | string? | enum | no | — | BLAST program to use [enum: blastp, blastn, blastx, tblastn, tblastx] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. |
| output_file | string | wsid | yes | — | Basename for the generated output files. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## MSA

**Label**: Multiple sequence alignment variation service
**Description**: Compute the multiple sequence alignment and analyze SNP/variance.
**File**: `tools/MSA.cwl`

### Inputs (7 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| fasta_files | string? | group | no | — |  |
| feature_groups | string? | wstype | no | — | Feature groups |
| aligner | string? | enum | no | "Muscle" | Tool used for aligning multiple sequences to each other. [enum: Muscle] |
| alphabet | string | enum | yes | "dna" | Determines which sequence alphabet is present. [enum: dna, protein] |
| fasta_keyboard_input | string? | string | no | — | Text input for a fasta file. |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## MetaCATS

**Label**: Metadata-driven Comparative Analysis Tool (meta-CATS)
**Description**: The meta-CATS tool looks for positions that significantly differ between user-defined groups of sequences.
**File**: `tools/MetaCATS.cwl`

### Inputs (6 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| alignment_file | string | wstype | yes | — | The location of the alignment file. |
| group_file | string | wstype | yes | — | The location of a file that partitions sequences into groups. |
| p_value | float | float | yes | 0.05 | The p-value cutoff for analyzing sequences. |
| alignment_type | string | enum | yes | — | The file format type. [enum: aligned_dna_fasta, aligned_protein_fasta] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. |
| output_file | string | wsid | yes | — | Basename for the generated output files. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## MetagenomeBinning

**Label**: Annotate metagenome data
**Description**: Assemble, bin, and annotate metagenomic sample data
**File**: `tools/MetagenomeBinning.cwl`

### Inputs (20 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| contigs | string? | wstype | no | — | Input set of DNA contigs for annotation |
| genome_group | string? | string | no | — | Name of genome group into whcih the generated genome ids will be placed.  |
| skip_indexing | boolean? | bool | no | false | If set, don't index the generated bins solr. They will not be available for analysis through the PATRIC site. |
| recipe | string? | string | no | — | Specifies a non-default annotation recipe for annotation of bins |
| viral_recipe | string? | string | no | — | Specifies a non-default annotation recipe for annotation of viral bins |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| force_local_assembly | boolean | bool | yes | false | If set, disable the use of remote clusters for assembly |
| force_inline_annotation | boolean? | bool | no | true | If set, disable the use of the cluster for annotation |
| perform_bacterial_binning | boolean? | bool | no | true | If set, perform bacterial binning |
| perform_viral_binning | boolean? | bool | no | false | If set, perform viral binning of any contings unbinned after bacterial binning |
| perform_viral_annotation | boolean? | bool | no | false | If set, perform viral annotation and loading of viral genomes into PATRIC database of any binned viruses |
| perform_bacterial_annotation | boolean? | bool | no | true | If set, perform bacterial annotation and loading of bacterial genomes into PATRIC database of any binned bacterial genomes |
| assembler | string? | string | no | — | If set, use the given assembler |
| danglen | string? | string | no | "50" | DNA kmer size for last-chance contig binning. Set to 0 to disable this step |
| min_contig_len | int? | int | no | 400 | Filter out short contigs |
| min_contig_cov | float? | float | no | 4 | Filter out contigs with low read depth in final assembly |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## MetagenomicReadMapping

**Label**: Metagenomic read mapping
**Description**: Map metagenomic reads to a defined gene set
**File**: `tools/MetagenomicReadMapping.cwl`

### Inputs (9 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| gene_set_type | string | enum | yes | — | Gene set type (predefined list / fasta file / feature group ) [enum: predefined_list, fasta_file, feature_group] |
| gene_set_name | string? | enum | no | — | Predefined gene set name [enum: MLST, CARD] |
| gene_set_fasta | string? | wstype | no | — | Protein data in FASTA format |
| gene_set_feature_group | string? | string | no | — | Name of feature group that defines the gene set  |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## ModelReconstruction

**Label**: Reconstruct metabolic model
**Description**: Reconstructs a metabolic model from an annotated genome.
**File**: `tools/ModelReconstruction.cwl`

### Inputs (6 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genome | string | wstype | yes | — | Input annotated genome for model reconstruction |
| media | string? | wstype | no | — | Media formulation in which model should be initially gapfilled |
| template_model | string? | wstype | no | — | Template upon which model should be constructed |
| fulldb | boolean | bool | yes | false | Add all reactions from template to model regardless of annotation |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## PhylogeneticTree

**Label**: Compute phylogenetic tree
**Description**: Computes a phylogenetic tree given a set of in-group and out-group genomes
**File**: `tools/PhylogeneticTree.cwl`

### Inputs (6 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_path | Directory | folder | yes | — | Path to which the output will be written.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. |
| in_genome_ids | string | list | yes | — | In-group genomes |
| out_genome_ids | string | list | yes | — | Out-group genomes |
| full_tree_method | string? | string | no | "ml" | Full tree method |
| refinement | string? | string | no | "yes" | Automated progressive refinement |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## PrimerDesign

**Label**: Primer Design
**Description**: Use Primer3 to design primers to given sequence
**File**: `tools/PrimerDesign.cwl`

### Inputs (24 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_file | string | wsid | yes | — | Basename for the generated output files. |
| output_path | Directory | folder | yes | — | Path to which the output will be written. |
| SEQUENCE_ID | string | string | yes | — | Sequence ID |
| SEQUENCE_TEMPLATE | string | string | yes | — | Nucleotide Sequence or (BVBRC Seq Id) |
| SEQUENCE_TARGET | string? | array | no | — | Start/stop of region that primers must flank |
| SEQUENCE_INCLUDED_REGION | string? | array | no | — | Region where primers can be picked |
| SEQUENCE_EXCLUDED_REGION | string? | array | no | — | Region where primers cannot overlap |
| SEQUENCE_OVERLAP_JUNCTION_LIST | string? | array | no | — | Start position and length of region that primers must flank |
| PRIMER_PRODUCT_SIZE_RANGE | string? | array | no | — | Min, max product size |
| PRIMER_NUM_RETURN | int? | integer | no | — | Max num primer pairs to report |
| PRIMER_MIN_SIZE | int? | integer | no | — | Min primer length |
| PRIMER_OPT_SIZE | int? | integer | no | — | Optimal primer length |
| PRIMER_MAX_SIZE | int? | integer | no | — | Maximum primer length |
| PRIMER_MAX_TM | float? | number | no | — | Maximum primer melting temperature |
| PRIMER_MIN_TM | float? | number | no | — | Minimum primer melting temperature |
| PRIMER_OPT_TM | float? | number | no | — | Optimal primer melting temperature |
| PRIMER_PAIR_MAX_DIFF_TM | float? | number | no | — | Max Tm difference of paired primers |
| PRIMER_MAX_GC | float? | number | no | — | Maximum primer GC percentage |
| PRIMER_MIN_GC | float? | number | no | — | Minimum primer GC percentage |
| PRIMER_OPT_GC | float? | number | no | — | Optimal primer GC percentage |
| PRIMER_SALT_MONOVALENT | float? | number | no | — | Concentration of monovalent cations (mM) |
| PRIMER_SALT_DIVALENT | float? | number | no | — | Concentration of divalent cations (mM) |
| PRIMER_DNA_CONC | float? | number | no | — | Annealing Oligo Concentration (nM) |
| PRIMER_DNTP_CONC | float? | number | no | — | Concentration of dNTPs (mM) |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## RASTJob

**Label**: Annotate genome for RAST
**Description**: RAST worker app.
**File**: `tools/RASTJob.cwl`

### Inputs (5 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| genome_object | string | wstype | yes | — | Input set of DNA contigs for annotation |
| output_path | Directory? | folder | no | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string? | wsid | no | — | Basename for the generated output files. Defaults to the basename of the input data. |
| workflow | string? | string | no | — | Specifies a custom workflow document (expert). |
| recipe | string? | string | no | — | Specifies a non-default annotation recipe |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## RNASeq

**Label**: Analyze RNASeq reads
**Description**: Align or assemble RNASeq reads into transcripts with normalized expression levels
**File**: `tools/RNASeq.cwl`

### Inputs (15 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| experimental_conditions | string? | string | no | — | Experimental conditions |
| contrasts | string? | string | no | — | Contrast list |
| strand_specific | boolean? | bool | no | true | Are the reads in this study strand-specific? |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_libs | string? | group | no | — |  |
| reference_genome_id | string | string | yes | — | Reference genome ID |
| genome_type | string | enum | yes | — | genome is type bacteria or host [enum: bacteria, host] |
| recipe | string | enum | yes | "HTSeq-DESeq" | Recipe used for RNAseq analysis [enum: HTSeq-DESeq, cufflinks, Host] |
| host_ftp | string? | string | no | — | Host FTP prefix for obtaining files |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data. |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| trimming | boolean? | bool | no | false | run trimgalore on the reads |
| unit_test | string? | string | no | — | Path to the json file used for unit testing |
| skip_sampling | string? | string | no | — | flag to skip the sampling step in alignment.py |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## RNASeq2

**Label**: Analyze RNASeq reads
**Description**: Align or assemble RNASeq reads into transcripts with normalized expression levels
**File**: `tools/RNASeq2.cwl`

### Inputs (14 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| experimental_conditions | string? | string | no | — | Experimental conditions |
| contrasts | string? | string | no | — | Contrast list |
| strand_specific | boolean? | bool | no | true | Are the reads in this study strand-specific? |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_libs | string? | group | no | — |  |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| genome_type | string? | enum | no | — | genome is type bacteria or host [enum: bacteria, host] |
| recipe | string? | enum | no | "RNA-Rocket" | Recipe used for RNAseq analysis [enum: RNA-Rocket, Rockhopper, Host] |
| host_ftp | string? | string | no | — | Host FTP prefix for obtaining files |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data. |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| unit_test | string? | string | no | — | Path to the json file used for unit testing |
| skip_sampling | string? | string | no | — | flag to skip the sampling step in alignment.py |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## RunProbModelSEEDJob

**Label**: Runs a ProbModelSEED job
**Description**: Runs a ProbModelSEED modeling job
**File**: `tools/RunProbModelSEEDJob.cwl`

### Inputs (4 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| command | string | string | yes | — | ProbModelSEED command to run |
| arguments | string | string | yes | — | ProbModelSEED arguments |
| output_path | Directory? | folder | no | — | Workspace folder for results (framework parameter) |
| output_file | string | string | yes | — | Prefix for output file names |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## SARS2Assembly

**Label**: Assemble SARS2 reads
**Description**: Assemble SARS2 reads into a consensus sequence
**File**: `tools/SARS2Assembly.cwl`

### Inputs (10 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| recipe | string? | enum | no | "auto" | Recipe used for assembly [enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore] |
| min_depth | int? | int | no | 100 | Minimum coverage to add reads to consensus sequence |
| max_depth | int? | int | no | 8000 | Maximum read depth to consider for consensus sequence |
| keep_intermediates | int? | int | no | 0 | Keep all intermediate output from the pipeline |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |
| debug_level | int? | int | no | 0 | Debugging level. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## SequenceSubmission

**Label**: Sequence Submission
**Description**: Sequence Submission
**File**: `tools/SequenceSubmission.cwl`

### Inputs (7 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_source | string | enum | yes | — | Source of input (id_list, fasta_data, fasta_file, genome_group) [enum: id_list, fasta_data, fasta_file, genome_group] |
| input_fasta_data | string? | string | no | — | Input sequence in fasta formats |
| input_fasta_file | string? | wsid | no | — | Input sequence as a workspace file of fasta data |
| input_genome_group | string? | wsid | no | — | Input sequence as a workspace genome group |
| metadata | string? | wsid | no | — | Metadata as a workspace file of csv |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## Sleep

**Label**: Sleep
**Description**: Sleep a bit.
**File**: `tools/Sleep.cwl`

### Inputs (3 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| sleep_time | int? | int | no | 10 | Time to sleep, in seconds. |
| output_path | Directory? | folder | no | — | Workspace folder for results (framework parameter) |
| output_file | string | string | yes | — | Prefix for output file names |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## SubspeciesClassification

**Label**: Subspecies Classification
**Description**: Subspecies Classification
**File**: `tools/SubspeciesClassification.cwl`

### Inputs (8 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_source | string | enum | yes | — | Source of input (id_list, fasta_data, fasta_file, genome_group) [enum: id_list, fasta_data, fasta_file, genome_group] |
| input_fasta_data | string? | string | no | — | Input sequence in fasta formats |
| input_fasta_file | string? | wsid | no | — | Input sequence as a workspace file of fasta data |
| input_genome_group | string? | wsid | no | — | Input sequence as a workspace genome group |
| ref_msa_fasta | string? | wsid | no | — | Reference multiple sequence alignment (Fasta-formatted) |
| virus_type | string | string | yes | — | Virus type |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## SyntenyGraph

**Label**: Compute synteny graph
**Description**: Computes a synteny graph
**File**: `tools/SyntenyGraph.cwl`

### Inputs (7 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| output_path | Directory | folder | yes | — | Path to which the output will be written.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. |
| genome_ids | string | list | yes | — | Input genomes |
| ksize | int | int | yes | 3 | Minimum neighborhood size for alignment |
| context | string | string | yes | "genome" | Context of alignment |
| diversity | string | string | yes | "species" | Diversity quotient calculation |
| alpha | string | string | yes | "patric_pgfam" | Alphabet to use to group genes |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## TaxonomicClassification

**Label**: classify reads
**Description**: Compute taxonomic classification for read data
**File**: `tools/TaxonomicClassification.cwl`

### Inputs (11 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| input_type | string | enum | yes | — | Input type (reads / contigs) [enum: reads, contigs] |
| contigs | string? | wstype | no | — | Input set of DNA contigs for classification |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| save_classified_sequences | boolean? | bool | no | false | Save the classified sequences |
| save_unclassified_sequences | boolean? | bool | no | false | Save the unclassified sequences |
| algorithm | string | enum | yes | "Kraken2" | Classification algorithm [enum: Kraken2] |
| database | string | enum | yes | "Kraken2" | Target database [enum: Default NT, Kraken2, Greengenes, RDP, SILVA] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data.  |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## TnSeq

**Label**: Analyze TnSeq data
**Description**: Use TRANSIT to analyze TnSeq data
**File**: `tools/TnSeq.cwl`

### Inputs (9 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| experimental_conditions | string? | list | no | — | Experimental conditions |
| contrasts | string? | list | no | — | Contrasts |
| read_files | string? | group | no | — |  |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| recipe | string? | enum | no | "gumbel" | Recipe used for TnSeq analysis [enum: griffin, tn5gaps, rankproduct, hmm, binomial, resampling] |
| protocol | string? | enum | no | "sassetti" | Protocol used for TnSeq analysis [enum: sassetti, tn5, mme1] |
| primer | string? | string | no | — | Primer DNA string for read trimming. |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data. |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

## Variation

**Label**: Identify SNVs
**Description**: Identify and annotate small nucleotide variations relative to a reference genome
**File**: `tools/Variation.cwl`

### Inputs (9 parameters)

| Parameter | CWL Type | BV-BRC Type | Required | Default | Description |
|-----------|----------|-------------|----------|---------|-------------|
| reference_genome_id | string | string | yes | — | Reference genome ID |
| paired_end_libs | string? | group | no | — |  |
| single_end_libs | string? | group | no | — |  |
| srr_ids | string? | string | no | — | Sequence Read Archive (SRA) Run ID |
| reference_genome_id | string? | string | no | — | Reference genome ID |
| mapper | string? | enum | no | "BWA-mem" | Tool used for mapping short reads against the reference genome [enum: BWA-mem, BWA-mem-strict, Bowtie2, MOSAIK, LAST] |
| caller | string? | enum | no | "FreeBayes" | Tool used for calling variations based on short read mapping [enum: FreeBayes, SAMtools] |
| output_path | Directory | folder | yes | — | Path to which the output will be written. Defaults to the directory containing the input data. |
| output_file | string | wsid | yes | — | Basename for the generated output files. Defaults to the basename of the input data. |

### Outputs (guessed)

| Output | CWL Type | Notes |
|--------|----------|-------|
| result | Directory | All BV-BRC output files |

### Review Notes

- [ ] Verify input types — complex params may need File or array types
- [ ] Identify expected output files for specific glob patterns
- [ ] Check default values are correct

---

