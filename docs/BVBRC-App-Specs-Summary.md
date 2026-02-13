# BV-BRC App Specs Summary

Generated: 2026-02-13
Source: 34 BV-BRC GitHub repos, 42 distinct apps

## Purpose

This document catalogs all BV-BRC application specs with their inputs (mapped to CWL types),
concrete output files (from service-script analysis), and special behaviors.
Use this to update `cwl/tools/*.cwl` files in GoWe.

## Overview

| # | App ID | Repo | CWL Exists | Category |
|---|--------|------|------------|----------|
| 1 | CEIRRDataSubmission | bvbrc_ceirr_data_submission | No | Submission |
| 2 | CodonTree | codon_trees | Yes | Phylogeny |
| 3 | ComparativeSystems | comparative_systems_service | Yes | Comparative |
| 4 | ComprehensiveGenomeAnalysis | p3_genome_annotation | Yes | Annotation |
| 5 | ComprehensiveSARS2Analysis | sars2_assembly | Yes | SARS2 |
| 6 | CoreGenomeMLST | bvbrc_CoreGenomeMLST | No | MLST |
| 7 | Date | bvbrc_standalone_apps | Yes | Utility |
| 8 | DifferentialExpression | bvbrc_differential_expression | Yes | Expression |
| 9 | FastqUtils | p3_fqutils | Yes | Reads |
| 10 | FluxBalanceAnalysis | p3_model_reconstruction | Yes | Metabolic |
| 11 | GapfillModel | p3_model_reconstruction | Yes | Metabolic |
| 12 | GeneTree | bvbrc_gene_tree | Yes | Phylogeny |
| 13 | GenomeAlignment | p3_mauve | Yes | Alignment |
| 14 | GenomeAnnotation | p3_genome_annotation | Yes | Annotation |
| 15 | GenomeAnnotationGenbank | p3_genome_annotation | Yes | Annotation |
| 16 | GenomeAnnotationGenbankTest | p3_genome_annotation | Yes | Annotation |
| 17 | GenomeAssembly | p3_assembly | Yes | Assembly |
| 18 | GenomeAssembly2 | p3_assembly | Yes | Assembly |
| 19 | GenomeComparison | bvbrc_proteome_comparison | Yes | Comparative |
| 20 | HASubtypeNumberingConversion | bvbrc_ha_subtype_conversion | Yes | Virus |
| 21 | Homology | homology_service | Yes | Search |
| 22 | MSA | p3_msa | Yes | Alignment |
| 23 | MetaCATS | bvbrc_metacats | Yes | Comparative |
| 24 | MetagenomeBinning | p3_binning | Yes | Metagenome |
| 25 | MetagenomicReadMapping | p3_metagenomic_read_mapping | Yes | Metagenome |
| 26 | ModelReconstruction | p3_model_reconstruction | Yes | Metabolic |
| 27 | PrimerDesign | bvbrc_primer_design | Yes | Design |
| 28 | RASTJob | p3_rast_app | Yes | Annotation |
| 29 | RNASeq | bvbrc_rnaseq | Yes | Expression |
| 30 | RunProbModelSEEDJob | p3_model_reconstruction | Yes | Metabolic |
| 31 | SARS2Assembly | sars2_assembly | Yes | SARS2 |
| 32 | SARS2Wastewater | bvbrc_SARS2Wastewater | No | SARS2 |
| 33 | SequenceSubmission | bvbrc_sequence_submission | Yes | Submission |
| 34 | Sleep | bvbrc_standalone_apps | Yes | Utility |
| 35 | SubspeciesClassification | bvbrc_subspecies_classification | Yes | Classification |
| 36 | SyntenyGraph | bvbrc_synteny_graph | Yes | Comparative |
| 37 | TaxonomicClassification | bvbrc_taxonomic_classification | Yes | Classification |
| 38 | TaxonomicClassification (v2) | bvbrc_taxonomic_classification_2 | (shared) | Classification |
| 39 | TnSeq | p3_tnseq | Yes | TnSeq |
| 40 | TreeSort | bvbrc_TreeSort | No | Phylogeny |
| 41 | Variation | bvbrc_variation | Yes | Variation |
| 42 | ViralAssembly | bvbrc_viral_assembly | No | Assembly |
| 43 | WholeGenomeSNPAnalysis | bvbrc_WholeGenomeSNPAnalysis | No | SNP |

### CWL Tools Without Research Data (existing but not in distinct repos)

- **FluxBalanceAnalysis** — likely in `bvbrc_standalone_apps` (App-FluxBalanceAnalysis.pl found there)
- **FunctionalClassification** — may be fetched via API but repo not identified
- **PhylogeneticTree** — may be `codon_trees` or a legacy name
- **RNASeq2** — may be a newer version of RNASeq in same or different repo

### Missing CWL Tools (need to be created)

- CEIRRDataSubmission
- CoreGenomeMLST
- SARS2Wastewater
- TreeSort
- ViralAssembly
- WholeGenomeSNPAnalysis

### Special Behaviors

| App | Behavior |
|-----|----------|
| Sleep | `donot_create_result_folder=1` — no outputs |
| GapfillModel | `donot_create_result_folder=1`, `donot_create_job_result=1` — ProbModelSEEDHelper manages workspace writes |
| ModelReconstruction | `donot_create_result_folder=1`, `donot_create_job_result=1` — same as GapfillModel |
| RunProbModelSEEDJob | `donot_create_result_folder=1`, `donot_create_job_result=1` — generic wrapper |
| ComprehensiveGenomeAnalysis | Wrapper — delegates to GenomeAssembly2 + GenomeAnnotation sub-jobs |
| ComprehensiveSARS2Analysis | Wrapper — delegates to SARS2Assembly + GenomeAnnotation sub-jobs |
| MetagenomeBinning | Creates per-bin GenomeAnnotation sub-jobs; epilog script with `donot_create_result_folder=1` |
| ComparativeSystems | Writes directly to hidden dot-prefix path (not result_folder) |
| ViralAssembly | Singular `paired_end_lib`/`single_end_lib`/`srr_id` (not plural) |
| MetagenomicReadMapping | Singular libs/srr_ids (not allow_multiple) |
| FastqUtils | Singular paired_end_libs/single_end_libs (not allow_multiple) |
| MetagenomeBinning | allow_multiple=false for paired_end_libs/single_end_libs/srr_ids |

---

## App Details

---

### CEIRRDataSubmission
- **Repo**: BV-BRC/bvbrc_ceirr_data_submission
- **Description**: CEIRR Data Submission
- **Script**: App-CEIRRDataSubmission

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| ceirr_data | string[] | yes | — | CEIRR data file in CSV format (allow_multiple, wsid) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.csv` → csv
- `*.log` → txt
- `*.txt` → txt
- `*.json` → json

#### Notes
Uses `p3-cp --recursive` with suffix mapping from work_dir to result_folder.

---

### CodonTree
- **Repo**: BV-BRC/codon_trees
- **Description**: Phylogenetic tree based on protein and DNA sequences of PGFams
- **Script**: App-CodonTree

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_ids | string[] | no | [] | Main genomes |
| genome_groups | string[] | no | [] | Genome groups |
| optional_genome_ids | string[] | no | [] | Optional genomes (not penalized for missing/duplicated genes) |
| genome_metadata_fields | string[] | no | — | Metadata fields to retrieve for each genome |
| number_of_genes | int | no | 20 | Desired number of genes |
| bootstraps | int | no | 100 | Number of bootstrap replicates |
| max_genomes_missing | int | no | 0 | Main genomes allowed missing from any PGFam |
| max_allowed_dups | int | no | 0 | Duplications allowed for main genomes in any PGFam |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.nwk` → nwk (Newick tree)
- `*.phyloxml` → phyloxml (PhyloXML tree)
- `*.png` → png (tree image)
- `*.svg` → svg (tree SVG)
- `*.html` → html (report)
- `*.txt` → txt (logs)
- `*.out` → txt (output logs)
- `*.err` → txt (error logs)

#### Notes
Uses `p3-cp` with suffix mapping. External tool: `p3x-build-codon-tree`.

---

### ComparativeSystems
- **Repo**: BV-BRC/comparative_systems_service
- **Description**: Create datastructures to decompose genomes
- **Script**: App-ComparativeSystems

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_ids | string[] | no | [] | Genome IDs |
| genome_groups | string[] | no | [] | Genome Groups |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.tsv` → tsv
- `*.json` → json
- `*.txt` → txt

#### Notes
SPECIAL: Writes directly to hidden dot-prefix path (`output_path/.output_file/`), NOT via `$app->result_folder()`. Uses shock for files > 10,000 bytes.

---

### ComprehensiveGenomeAnalysis
- **Repo**: BV-BRC/p3_genome_annotation
- **Description**: Analyze a genome from reads or contigs, generating a detailed analysis report
- **Script**: App-ComprehensiveGenomeAnalysis

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_type | string | yes | — | enum: reads, contigs, genbank |
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore], interleaved boolean, read_orientation_outward boolean, insert_size_mean int, insert_size_stdev float |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore] |
| srr_ids | string[] | no | — | SRA Run IDs |
| reference_assembly | File | no | — | Reference contigs (wstype: Contigs) |
| recipe | string | no | auto | Assembly recipe; enum: auto, unicycler, canu, spades, meta-spades, plasmid-spades, single-cell, flye |
| racon_iter | int | no | 2 | Racon polishing iterations |
| pilon_iter | int | no | 2 | Pilon polishing iterations |
| trim | boolean | no | false | Trim reads before assembly |
| normalize | boolean | no | false | Normalize reads (BBNorm) |
| filtlong | boolean | no | false | Filter long reads |
| target_depth | int | no | 200 | Target depth for BBNorm/Filtlong |
| genome_size | int | no | 5000000 | Estimated genome size |
| min_contig_len | int | no | 300 | Min contig length |
| min_contig_cov | float | no | 5 | Min contig coverage |
| gto | File | no | — | Preannotated genome (wstype: Genome) |
| genbank_file | File | no | — | Genome file (wstype: genbank_file) |
| contigs | File | no | — | Contigs (wstype: Contigs) |
| scientific_name | string | yes | — | Scientific name |
| taxonomy_id | int | no | — | NCBI Taxonomy ID |
| code | int | yes | 0 | Genetic code; enum: 0, 1, 4, 11, 25 |
| domain | string | yes | auto | enum: Bacteria, Archaea, Viruses, auto |
| reference_genome_id | string | no | — | Reference genome ID |
| workflow | string | no | — | Custom workflow (expert) |
| analyze_quality | boolean | no | — | Run quality analysis |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `FullGenomeReport.html` → html (full report)
- `annotated.genome` → genome (annotated genome object)
- `circos.svg` → svg (Circos plot)
- `circos.png` → png (Circos image)
- `subsystem_colors.json` → json
- `quality.json` → json
- `.assembly/` → folder (assembly sub-job)
- `.annotation/` → folder (annotation sub-job)
- `.annotation/annotation.genome` → genome

#### Notes
Wrapper app — delegates to GenomeAssembly2 + GenomeAnnotation sub-jobs. Sub-jobs write to hidden folders.

---

### ComprehensiveSARS2Analysis
- **Repo**: BV-BRC/sars2_assembly
- **Description**: SARS-CoV-2 genome analysis from reads or contigs
- **Script**: App-ComprehensiveSARS2Analysis

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_type | string | yes | — | enum: reads, contigs, genbank |
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/nanopore/iontorrent], interleaved boolean, read_orientation_outward boolean |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/nanopore/iontorrent] |
| srr_ids | string[] | no | — | SRA Run IDs |
| recipe | string | no | auto | enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore |
| primers | string | yes | ARTIC | enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long |
| primer_version | string | no | — | Primer version |
| min_depth | int | no | 100 | Minimum coverage |
| keep_intermediates | int | no | 0 | Keep intermediates |
| genbank_file | File | no | — | wstype: genbank_file |
| contigs | File | no | — | wstype: Contigs |
| scientific_name | string | yes | — | Scientific name |
| taxonomy_id | int | yes | — | NCBI Taxonomy ID |
| code | string | yes | 1 | Genetic code; enum: 11, 4, 1 |
| domain | string | yes | Viruses | enum: Bacteria, Archaea, Viruses |
| reference_genome_id | string | no | — | Reference genome ID |
| reference_virus_name | string | no | — | Reference virus name |
| workflow | string | no | — | Custom workflow (expert) |
| analyze_quality | boolean | no | — | Run quality analysis |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `FullGenomeReport.html` → html (full report)
- `annotated.genome` → genome
- `{output_base}.fasta` → contigs (consensus FASTA)
- `.assembly/` → folder (assembly sub-job)
- `.annotation/` → folder (annotation sub-job)

#### Notes
Wrapper app — delegates to SARS2Assembly + GenomeAnnotation sub-jobs.

---

### CoreGenomeMLST
- **Repo**: BV-BRC/bvbrc_CoreGenomeMLST
- **Description**: Evaluate core genomes from a set of genome groups of the same species
- **Script**: App-CoreGenomeMLST

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_genome_type | string | yes | genome_group | enum: genome_group, genome_fasta |
| analysis_type | string | yes | chewbbaca | enum: chewbbaca |
| input_genome_group | string | no | — | Genome group name |
| input_genome_fasta | File | no | — | FASTA data (wstype: genome_fasta) |
| schema_location | string | no | — | Schema parent directory path |
| input_schema_selection | string | yes | — | Species schema to compare against |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.tre` → nwk (tree files)
- `*.tsv` → tsv
- `*.NJ` → txt (Neighbor-joining)
- `*.ML` → txt (Max-likelihood)
- `*.vcf` → vcf
- `*.fasta` → contigs
- `*.html` → html (report)

#### Notes
Uses `p3-cp --recursive` with suffix mapping: tre→nwk, tsv→tsv, NJ→txt, ML→txt, vcf→vcf, fasta→contigs, html→html.

---

### Date
- **Repo**: BV-BRC/bvbrc_standalone_apps
- **Description**: Returns the current date and time
- **Script**: App-Date

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |

#### Concrete Outputs
- `now` → txt (date string)

---

### DifferentialExpression
- **Repo**: BV-BRC/bvbrc_differential_expression
- **Description**: Parses and transforms differential expression data
- **Script**: App-DifferentialExpression

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| xfile | File | yes | — | Experiment data file (wstype: ExpList) |
| mfile | File | no | — | Metadata file (wstype: ExpMetadata) |
| ustring | string | yes | — | User information (JSON string) |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |

#### Concrete Outputs
- `experiment.json` → diffexp_experiment
- `expression.json` → diffexp_expression
- `mapping.json` → diffexp_mapping
- `sample.json` → diffexp_sample

---

### FastqUtils
- **Repo**: BV-BRC/p3_fqutils
- **Description**: Common processing of FASTQ files
- **Script**: App-FastqUtils

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| reference_genome_id | string | no | — | Reference genome ID |
| paired_end_libs | record | no | — | SINGULAR (not array). Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore] |
| single_end_libs | record | no | — | SINGULAR (not array). Fields: read File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore] |
| srr_libs | record[] | no | — | Fields: srr_accession string |
| recipe | string[] | yes | [] | Recipe(s) to apply |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.bam` → bam
- `*.fasta` → feature_protein_fasta
- `*.fastq` / `*.fq` → reads
- `*.fastq.gz` / `*.fq.gz` → reads
- `*.html` → html
- `*.png` → png
- `*.tsv` → tsv
- `*.txt` → txt

#### Notes
paired_end_libs and single_end_libs are NOT allow_multiple — single record, not array.

---

### GapfillModel
- **Repo**: BV-BRC/p3_model_reconstruction
- **Description**: Run gapfilling on metabolic model
- **Script**: App-GapfillModel

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| model | File | yes | — | Model object (wstype: model) |
| media | File | no | — | Media formulation (wstype: media) |
| probanno | File | no | — | Probabilistic annotation (wstype: probanno) |
| alpha | float | no | 0 | Comprehensive gapfilling priority |
| allreversible | boolean | no | false | Make all reactions reversible |
| allowunbalanced | boolean | no | false | Allow unbalanced reactions |
| integrate_solution | boolean | no | false | Integrate first solution |
| thermo_const_type | string | no | — | enum: None, Simple |
| media_supplement | string[] | no | — | Additional media compounds |
| geneko | string[] | no | — | Gene knockouts |
| rxnko | string[] | no | — | Reaction knockouts |
| target_reactions | string[] | no | — | Target reactions for gapfilling |
| objective_fraction | float | no | 0.001 | Objective fraction |
| low_expression_theshold | float | no | 1 | Low expression threshold |
| high_expression_theshold | float | no | 1 | High expression threshold |
| output_file | string | no | — | File basename |
| uptake_limit | record[] | no | — | Fields: atom string[enum: C,N,O,P,S], maxuptake float |
| custom_bounds | record[] | no | — | Fields: vartype string[enum: flux,biomassflux,drainflux], variable string, upperbound float, lowerbound float |
| objective | record[] | no | — | Fields: vartype string[enum: flux,biomassflux,drainflux], variable string, coefficient float |

#### Concrete Outputs
None — managed by ProbModelSEEDHelper internally.

#### Notes
`donot_create_result_folder=1`, `donot_create_job_result=1`. No output_path. No standard result_folder.

---

### GeneTree
- **Repo**: BV-BRC/bvbrc_gene_tree
- **Description**: Estimate phylogeny of gene or other sequence feature
- **Script**: App-GeneTree

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| sequences | string[] | yes | — | Sequence data inputs |
| alignment_program | string | no | — | enum: muscle, mafft |
| trim_threshold | float | no | — | Alignment end-trimming threshold |
| gap_threshold | float | no | — | Delete gappy sequences threshold |
| alphabet | string | yes | — | enum: DNA, Protein |
| substitution_model | string | no | — | enum: HKY85, JC69, K80, F81, F84, TN93, GTR, LG, WAG, JTT, etc. |
| bootstrap | int | no | — | Bootstrapping |
| recipe | string | no | RAxML | enum: RAxML, PhyML, FastTree |
| tree_type | string | no | — | enum: viral_genome, gene |
| feature_metadata_fields | string[] | no | — | Gene metadata fields |
| genome_metadata_fields | string[] | no | — | Genome metadata fields |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `{output_file}_unaligned.fa` → unspecified (if alignment done)
- `{output_file}_aligned.fa` → aligned_dna_fasta or aligned_protein_fasta
- `{output_file}_{recipe}_tree.nwk` → nwk (tree)
- `{output_file}_{recipe}_log.txt` → txt (log)
- `{output_file}*.phyloxml` → phyloxml
- `{output_file}_gene_tree_report.html` → html (report)
- `{tree_graphic_file}` → svg or png

---

### GenomeAlignment
- **Repo**: BV-BRC/p3_mauve
- **Description**: Multiple whole genome alignment with rearrangements (Mauve)
- **Script**: App-GenomeAlignment

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_ids | string[] | yes | [] | Genome IDs to align |
| recipe | string | no | progressiveMauve | enum: progressiveMauve, mauveAligner |
| seedWeight | float | no | — | Seed weight for initial anchors |
| maxGappedAlignerLength | float | no | — | Max bp for gapped aligner |
| maxBreakpointDistanceScale | float | no | — | Max breakpoint distance scale [0,1] |
| conservationDistanceScale | float | no | — | Conservation distance scale [0,1] |
| weight | float | no | — | Minimum pairwise LCB score |
| minScaledPenalty | float | no | — | Min breakpoint penalty after scaling |
| hmmPGoHomologous | float | no | — | HMM transition probability (unrelated→homologous) |
| hmmPGoUnrelated | float | no | — | HMM transition probability (homologous→unrelated) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.xmfa` → txt (XMFA alignment)
- `*.json` → json (metadata)

---

### GenomeAnnotation
- **Repo**: BV-BRC/p3_genome_annotation
- **Description**: Calls genes and functionally annotates input contig set
- **Script**: App-GenomeAnnotation

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| contigs | File | yes | — | Input contigs (wstype: Contigs) |
| scientific_name | string | yes | — | Scientific name |
| taxonomy_id | int | no | — | NCBI Taxonomy ID |
| code | int | yes | 0 | Genetic code; enum: 0, 1, 4, 11, 25 |
| domain | string | yes | auto | enum: Bacteria, Archaea, Viruses, auto |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |
| reference_genome_id | string | no | — | Reference genome ID |
| reference_virus_name | string | no | — | Reference virus name |
| fix_errors | boolean | no | — | Fix errors (overlapping RNAs, embedded genes) |
| fix_frameshifts | boolean | no | — | Fix frameshifts |
| workflow | string | no | — | Custom workflow (expert) |
| recipe | string | no | — | Annotation recipe |
| analyze_quality | boolean | no | — | Run quality analysis |
| assembly_output | Directory | no | — | Workspace path to assembly output |
| custom_pipeline | record | no | — | RASTtk pipeline config (nested: call-features-rRNA-SEED, call-features-tRNA-trnascan, etc.) |

#### Concrete Outputs
- `{output_file}.genome` → genome (annotated genome object)
- `quality.json` → json
- `load_files/` → folder (indexing files)
- `load_files/*.json` → json

---

### GenomeAnnotationGenbank
- **Repo**: BV-BRC/p3_genome_annotation
- **Description**: Annotate genome from GenBank file
- **Script**: App-GenomeAnnotationGenbank

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genbank_file | File | yes | — | wstype: genbank_file |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |
| reference_virus_name | string | no | — | Reference virus name |
| workflow | string | no | — | Custom workflow (expert) |
| recipe | string | no | — | Annotation recipe |
| scientific_name | string | no | — | Scientific name (overrides genbank) |
| taxonomy_id | int | no | — | NCBI Taxonomy ID (overrides genbank) |
| code | string | no | — | Genetic code; enum: 11, 4, 25 |
| domain | string | no | Bacteria | enum: Bacteria, Archaea |
| import_only | boolean | no | — | Import without reannotation |
| raw_import_only | boolean | no | — | Import without postprocessing |
| skip_contigs | boolean | no | — | Do not import contigs |
| fix_errors | boolean | no | — | Fix errors |
| fix_frameshifts | boolean | no | — | Fix frameshifts |
| custom_pipeline | record | no | — | RASTtk pipeline config |

#### Concrete Outputs
Same as GenomeAnnotation.

---

### GenomeAnnotationGenbankTest
- **Repo**: BV-BRC/p3_genome_annotation
- **Description**: Annotate genome from GenBank (test version)
- **Script**: App-GenomeAnnotationGenbankTest

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genbank_file | File | yes | — | wstype: genbank_file |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |
| fix_errors | boolean | no | — | Fix errors |
| fix_frameshifts | boolean | no | — | Fix frameshifts |
| custom_pipeline | record | no | — | RASTtk pipeline config |

#### Concrete Outputs
Same as GenomeAnnotation.

---

### GenomeAssembly
- **Repo**: BV-BRC/p3_assembly
- **Description**: Assemble reads into contigs (legacy)
- **Script**: App-GenomeAssembly

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/nanopore], interleaved boolean, read_orientation_outward boolean, insert_size_mean int, insert_size_stdev float |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/nanopore] |
| srr_ids | string[] | no | — | SRA Run IDs |
| reference_assembly | File | no | — | Reference contigs (wstype: Contigs) |
| recipe | string | no | auto | enum: auto, full_spades, fast, miseq, smart, kiki |
| pipeline | string | no | — | Advanced pipeline (overrides recipe) |
| min_contig_len | int | no | 300 | Min contig length |
| min_contig_cov | float | no | 5 | Min contig coverage |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `contigs.fa` → contigs (assembled contigs)
- `{job_id}_analysis.zip` → zip (if non-empty)
- `report.txt` → txt (assembly report)

#### Notes
Legacy assembly app using ARAST. Output name hardcoded as "contigs".

---

### GenomeAssembly2
- **Repo**: BV-BRC/p3_assembly
- **Description**: Assemble WGS reads into contigs
- **Script**: App-GenomeAssembly2

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore], interleaved boolean, read_orientation_outward boolean |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/pacbio_hifi/nanopore] |
| srr_ids | string[] | no | — | SRA Run IDs |
| max_bases | int | no | 10000000000 | Max bases triggering downsampling |
| recipe | string | no | auto | enum: auto, unicycler, flye, meta-flye, canu, spades, meta-spades, plasmid-spades, single-cell, megahit |
| racon_iter | int | no | 2 | Racon polishing iterations |
| pilon_iter | int | no | 2 | Pilon polishing iterations |
| trim | boolean | no | false | Trim reads |
| target_depth | int | no | 200 | Target depth |
| normalize | boolean | no | false | Normalize reads (BBNorm) |
| filtlong | boolean | no | false | Filter long reads |
| genome_size | int | no | 5000000 | Estimated genome size |
| min_contig_len | int | no | 300 | Min contig length |
| min_contig_cov | float | no | 5 | Min contig coverage |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `p3x-assembly.stdout` → txt
- `p3x-assembly.stderr` → txt
- `*` from `p3_assembly_work/save/` → various (recursive copy with type_map)

#### Notes
The `save` directory contains all output files. `max_bases` appears twice in the spec (duplicate).

---

### GenomeComparison
- **Repo**: BV-BRC/bvbrc_proteome_comparison
- **Description**: Compare proteome sets from multiple genomes using BLAST
- **Script**: App-GenomeComparison

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_ids | string[] | no | — | Genome IDs |
| user_genomes | File[] | no | — | Protein FASTA files (wstype: feature_protein_fasta) |
| user_feature_groups | File[] | no | — | Feature groups (wstype: feature_group) |
| reference_genome_index | int | no | 1 | Reference genome index (1-based) |
| min_seq_cov | float | no | 0.30 | Min coverage |
| max_e_val | float | no | 1e-5 | Max E-value |
| min_ident | float | no | 0.1 | Min fraction identity |
| min_positives | float | no | 0.2 | Min fraction positive positions |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `genome_comparison.txt` → genome_comparison_table
- `genome_comparison.xls` → xls
- `genome_comparison.json` → json
- `circos/ref_genome.txt` → txt
- `circos/comp_genome_{N}.txt` → txt (per comparison genome)
- `circos/karyotype.txt` → txt
- `circos/large.tiles.txt` → txt
- `circos/circos.svg` → svg
- `circos/legend.html` → html
- `circos/circos_final.html` → html

---

### HASubtypeNumberingConversion
- **Repo**: BV-BRC/bvbrc_ha_subtype_conversion
- **Description**: HA Subtype Numbering Conversion
- **Script**: App-HASubtypeNumberingConversion

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_source | string | yes | — | enum: feature_list, fasta_data, fasta_file, feature_group |
| input_fasta_data | string | no | — | FASTA data |
| input_fasta_file | string | no | — | Workspace FASTA file |
| input_feature_group | string | no | — | Feature group |
| input_feature_list | string | no | — | Feature IDs |
| types | string | yes | — | Selected types |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.out` → txt
- `*.tsv` → tsv
- `*.fasta` → contigs

#### Notes
Uses `p3-cp -r` with suffix mapping: out→txt, tsv→tsv, fasta→contigs.

---

### Homology
- **Repo**: BV-BRC/homology_service
- **Description**: Perform homology searches on sequence data
- **Script**: App-Homology

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_type | string | yes | — | enum: dna, aa |
| input_source | string | yes | — | enum: id_list, fasta_data, fasta_file, feature_group, genome_group_features, genome_group_sequences |
| input_fasta_data | string | no | — | FASTA input |
| input_id_list | string[] | no | — | Sequence IDs |
| input_fasta_file | string | no | — | Workspace FASTA file (wsid) |
| input_feature_group | string | no | — | Feature group (wsid) |
| input_genome_group | string | no | — | Genome group (wsid) |
| db_type | string | yes | — | enum: faa, ffn, frn, fna |
| db_source | string | yes | — | enum: id_list, fasta_data, fasta_file, genome_list, feature_group, genome_group, taxon_list, precomputed_database |
| db_fasta_data | string | no | — | Database FASTA |
| db_fasta_file | string | no | — | Database file (wsid) |
| db_id_list | string[] | no | — | Database IDs |
| db_feature_group | string | no | — | Database feature group (wsid) |
| db_genome_group | string | no | — | Database genome group (wsid) |
| db_genome_list | string[] | no | — | Database genome list |
| db_taxon_list | string[] | no | — | Database taxon list |
| db_precomputed_database | string | no | — | Precomputed database name |
| blast_program | string | no | — | enum: blastp, blastn, blastx, tblastn, tblastx |
| blast_evalue_cutoff | float | no | 1e-5 | E-value cutoff |
| blast_max_hits | int | no | 300 | Max hits |
| blast_min_coverage | int | no | — | Min coverage |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `blast_out.raw.json` → json (raw BLAST JSON)
- `blast_out.txt` → txt (tabular BLAST output)
- `blast_headers.txt` → txt (column headers)
- `blast_out.json` → json (processed BLAST JSON)
- `blast_out.metadata.json` → json (search metadata)

---

### MetaCATS
- **Repo**: BV-BRC/bvbrc_metacats
- **Description**: Metadata-driven Comparative Analysis Tool (meta-CATS)
- **Script**: App-MetaCATS

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| p_value | float | yes | 0.05 | P-value cutoff |
| year_ranges | string | no | — | Year ranges |
| metadata_group | string | no | — | Metadata type |
| input_type | string | yes | — | enum: auto, groups, files |
| alphabet | string | yes | na | enum: na, aa |
| groups | string[] | no | [] | Feature groups (wstype: feature_group) |
| alignment_file | File | no | — | Alignment file (wstype: feature_protein_fasta) |
| group_file | File | no | — | Group file (wstype: tsv) |
| alignment_type | string | no | — | enum: aligned_dna_fasta, aligned_protein_fasta |
| auto_groups | record[] | no | — | Fields: id string, grp string, g_id string, metadata string |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*Table.tsv` → tsv (results table)
- `*.log` → txt
- `*.afa` → aligned_protein_fasta

---

### MetagenomeBinning
- **Repo**: BV-BRC/p3_binning
- **Description**: Assemble, bin, and annotate metagenomic sample data
- **Script**: App-MetagenomeBinning

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_libs | record | no | — | SINGULAR (allow_multiple=false). Fields: read1 File, read2 File |
| single_end_libs | record | no | — | SINGULAR (allow_multiple=false). Fields: read File |
| srr_ids | string | no | — | SINGULAR (allow_multiple=false) |
| contigs | File | no | — | Input contigs (wstype: Contigs) |
| genome_group | string | no | — | Genome group name |
| recipe | string | no | — | Annotation recipe |
| viral_recipe | string | no | — | Viral annotation recipe |
| force_local_assembly | boolean | yes | false | Disable remote assembly |
| force_inline_annotation | boolean | no | true | Disable cluster annotation |
| perform_bacterial_binning | boolean | no | true | Perform bacterial binning |
| perform_viral_binning | boolean | no | false | Perform viral binning |
| perform_viral_annotation | boolean | no | false | Perform viral annotation |
| perform_bacterial_annotation | boolean | no | true | Perform bacterial annotation |
| assembler | string | no | "" | Assembler to use |
| danglen | string | no | 50 | DNA kmer size for binning |
| min_contig_len | int | no | 400 | Min contig length |
| min_contig_cov | float | no | 4 | Min contig coverage |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `contigs.fasta` → contigs
- `spades.log` → txt (or megahit log)
- `params.txt` → txt
- `coverage.stats.txt` → txt
- `unbinned.fasta` → contigs
- `unplaced.fasta` → contigs
- `bins.stats.txt` → txt
- `bins.json` → json
- `BinningReport.html` → html
- `ViralBinningReport.html` → html (if viral binning)
- `{bin_name}.fasta` → contigs (per-bin FASTA)
- `.{bin_base_name}/` → folder (per-bin annotation sub-jobs)

#### Notes
Epilog script with `donot_create_result_folder=1`. Creates per-bin annotation sub-jobs. allow_multiple=false for lib inputs.

---

### MetagenomicReadMapping
- **Repo**: BV-BRC/p3_metagenomic_read_mapping
- **Description**: Map metagenomic reads to a defined gene set
- **Script**: App-MetagenomicReadMapping

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| gene_set_type | string | yes | — | enum: predefined_list, fasta_file, feature_group |
| gene_set_name | string | no | — | enum: VFDB, CARD, feature_group, fasta_file |
| gene_set_fasta | File | no | — | Protein FASTA (wstype: feature_protein_fasta) |
| gene_set_feature_group | string | no | — | Feature group name |
| paired_end_libs | record | no | — | SINGULAR. Fields: read1 File, read2 File |
| single_end_libs | record | no | — | SINGULAR. Fields: read File |
| srr_ids | string | no | — | SINGULAR |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `kma.res` → txt (KMA results)
- `kma.aln` → txt (KMA alignments)
- `kma.fsa` → txt (KMA consensus)
- `kma.frag` → txt (KMA fragments)
- `MetagenomicReadMappingReport.html` → html (report)

#### Notes
Singular inputs (not allow_multiple). Uses KMA for mapping.

---

### ModelReconstruction
- **Repo**: BV-BRC/p3_model_reconstruction
- **Description**: Reconstruct metabolic model from annotated genome
- **Script**: App-ModelReconstruction

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome | File | yes | — | Annotated genome (wstype: genome) |
| media | File | no | — | Gapfilling media (wstype: media) |
| template_model | File | no | — | Template model (wstype: template_model) |
| fulldb | boolean | yes | false | Add all reactions from template |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |

#### Concrete Outputs
None — managed by ProbModelSEEDHelper internally.

#### Notes
`donot_create_result_folder=1`, `donot_create_job_result=1`.

---

### MSA
- **Repo**: BV-BRC/p3_msa
- **Description**: Multiple sequence alignment and SNP/variance analysis
- **Script**: App-MSA

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_status | string | no | "" | enum: unaligned, aligned |
| input_type | string | no | "" | enum: input_group, input_fasta, input_sequence, input_genomegroup, input_featurelist, input_genomelist |
| fasta_files | record[] | no | — | Fields: file File(wstype: feature_protein_fasta), type string[enum: feature_dna_fasta, feature_protein_fasta] |
| select_genomegroup | File[] | no | — | Genome groups (wstype: genome_group) |
| feature_groups | File[] | no | — | Feature groups (wstype: feature_group) |
| feature_list | string[] | no | — | Feature list |
| genome_list | string[] | no | — | Genome list |
| aligner | string | no | Muscle | enum: Muscle, Mafft, progressiveMauve |
| alphabet | string | yes | dna | enum: dna, protein |
| fasta_keyboard_input | string | no | "" | Text FASTA input |
| ref_type | string | no | none | enum: none, string, feature_id, genome_id, first |
| strategy | string | no | auto | Mafft strategy; enum: auto, fftns1, fftns2, fftnsi, einsi, linsi, ginsi |
| ref_string | string | no | "" | Reference sequence identity |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `{prefix}.afa` → aligned_dna_fasta or aligned_protein_fasta
- `{prefix}.consensus.fasta` → txt
- `{prefix}.snp.tsv` → tsv
- `{prefix}_fasttree.nwk` → nwk
- `{prefix}_midpoint.nwk` → nwk (optional)
- `muscle.job.log` or `mafft.job.log` → txt

---

### PrimerDesign
- **Repo**: BV-BRC/bvbrc_primer_design
- **Description**: Primer3-based primer design
- **Script**: App-PrimerDesign

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_type | string | yes | — | enum: sequence_text, workplace_fasta, database_id |
| sequence_input | string | yes | — | DNA sequence data |
| SEQUENCE_ID | string | no | — | Sequence ID |
| SEQUENCE_TARGET | string[] | no | — | Amplification target (start/stop) |
| SEQUENCE_INCLUDED_REGION | string[] | no | — | Region where primers can be picked |
| SEQUENCE_EXCLUDED_REGION | string[] | no | — | Region where primers cannot overlap |
| SEQUENCE_OVERLAP_JUNCTION_LIST | string[] | no | — | Junction overlap list |
| PRIMER_PICK_INTERNAL_OLIGO | int | no | — | Pick internal oligo (1=yes) |
| PRIMER_PRODUCT_SIZE_RANGE | string[] | no | — | Min/max product size |
| PRIMER_NUM_RETURN | int | no | — | Max primer pairs to report |
| PRIMER_MIN_SIZE | int | no | — | Min primer length |
| PRIMER_OPT_SIZE | int | no | — | Optimal primer length |
| PRIMER_MAX_SIZE | int | no | — | Max primer length |
| PRIMER_MAX_TM | float | no | — | Max melting temperature |
| PRIMER_MIN_TM | float | no | — | Min melting temperature |
| PRIMER_OPT_TM | float | no | — | Optimal melting temperature |
| PRIMER_PAIR_MAX_DIFF_TM | float | no | — | Max Tm difference |
| PRIMER_MAX_GC | float | no | — | Max GC% |
| PRIMER_MIN_GC | float | no | — | Min GC% |
| PRIMER_OPT_GC | float | no | — | Optimal GC% |
| PRIMER_SALT_MONOVALENT | float | no | — | Monovalent cation concentration (mM) |
| PRIMER_SALT_DIVALENT | float | no | — | Divalent cation concentration (mM) |
| PRIMER_DNA_CONC | float | no | — | Oligo concentration (nM) |
| PRIMER_DNTP_CONC | float | no | — | dNTP concentration (mM) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `{output_file}_Primer3_input.txt` → txt
- `{output_file}_Primer3_output.txt` → txt
- `{output_file}_dynamic_report.html` → html (if primers found)
- `{output_file}_table.html` → html
- `{output_file}_primers.fasta` → Feature_DNA_FASTA (if primers found)

---

### RASTJob
- **Repo**: BV-BRC/p3_rast_app
- **Description**: RAST annotation worker app
- **Script**: App-RASTJob

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_object | File | yes | — | Input genome (wstype: string) |
| output_path | Directory | no | — | Output folder |
| output_file | string | no | — | File basename |
| workflow | string | no | — | Custom workflow (expert) |
| recipe | string | no | — | Annotation recipe |

#### Concrete Outputs
- `{output_file}` → genome (annotated genome object JSON)

---

### RNASeq
- **Repo**: BV-BRC/bvbrc_rnaseq
- **Description**: Align or assemble RNASeq reads into transcripts with normalized expression
- **Script**: App-RNASeq

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| experimental_conditions | string[] | no | — | Experimental conditions |
| contrasts | string | no | — | Contrast list |
| strand_specific | boolean | no | true | Strand-specific reads |
| paired_end_libs | record[] | no | — | Fields: sample_id string, read1 File, read2 File, interleaved boolean, insert_size_mean int, insert_size_stdev float, condition int |
| single_end_libs | record[] | no | — | Fields: sample_id string, read File, condition int |
| srr_libs | record[] | no | — | Fields: sample_id string, srr_accession string, condition int |
| reference_genome_id | string | yes | — | Reference genome ID |
| genome_type | string | yes | — | enum: bacteria, host |
| recipe | string | yes | HTSeq-DESeq | enum: HTSeq-DESeq, cufflinks, Host |
| host_ftp | string | no | — | Host FTP prefix |
| trimming | boolean | no | false | Run trimgalore |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.bam` → bam (per sample)
- `*.bam.bai` → bai (per sample)
- `*.txt` → txt
- `gene_exp.gmx` → diffexp_input_data
- `{output_name}{dsuffix}` → job_result (diffexp sub-job result)
- `.{output_name}{dsuffix}/` → folder (diffexp results)
- `summary.txt` → txt

#### Notes
Creates differential expression sub-job with hidden folder convention.

---

### RunProbModelSEEDJob
- **Repo**: BV-BRC/p3_model_reconstruction
- **Description**: Generic ProbModelSEED job runner
- **Script**: App-RunProbModelSEEDJob

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| command | string | yes | — | ProbModelSEED command |
| arguments | string | yes | — | ProbModelSEED arguments |

#### Concrete Outputs
None — managed by ProbModelSEEDHelper internally.

#### Notes
`donot_create_result_folder=1`, `donot_create_job_result=1`. No output_path/output_file.

---

### SARS2Assembly
- **Repo**: BV-BRC/sars2_assembly
- **Description**: Assemble SARS-CoV-2 reads into consensus sequence
- **Script**: App-SARS2Assembly

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/nanopore/iontorrent], interleaved boolean, read_orientation_outward boolean |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/nanopore/iontorrent] |
| srr_ids | string[] | no | — | SRA Run IDs |
| recipe | string | no | auto | enum: auto, onecodex, cdc-illumina, cdc-nanopore, artic-nanopore |
| primers | string | yes | ARTIC | enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long |
| primer_version | string | no | — | Primer version |
| min_depth | int | no | 100 | Min coverage |
| max_depth | int | no | 8000 | Max read depth |
| keep_intermediates | int | no | 0 | Keep intermediates |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `{output_file}.fasta` → contigs (consensus)
- `assembly-details.json` → json
- `sra-metadata/` → folder
- `*` from assembly dir → various (recursive copy via write_dir)

---

### SARS2Wastewater
- **Repo**: BV-BRC/bvbrc_SARS2Wastewater
- **Description**: SARS-CoV-2 wastewater surveillance (Freyja lineage deconvolution)
- **Script**: App-SARS2Wastewater

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_libs | record[] | no | — | Fields: sample_id string, read1 File, read2 File, platform string[enum: infer/illumina/pacbio/nanopore/iontorrent], interleaved boolean, read_orientation_outward boolean, primers string, primer_version string, sample_level_date string |
| single_end_libs | record[] | no | — | Fields: sample_id string, read File, platform string, primers string, primer_version string, sample_level_date string |
| srr_libs | record[] | no | — | Fields: sample_id string, srr_accession string, primers string, primer_version string, sample_level_date string |
| recipe | string | no | auto | enum: onecodex |
| primers | string | yes | ARTIC | enum: ARTIC, midnight, qiagen, swift, varskip, varskip-long |
| minimum_base_quality_score | int | no | 20 | Freyja --minq |
| minimum_genome_coverage | int | no | 60 | Freyja --mincov |
| agg_minimum_lineage_abundance | float | no | 0.01 | Freyja --thresh |
| minimum_coverage_depth | int | no | 0 | Freyja --depthcutoff |
| confirmedonly | boolean | no | false | Exclude unconfirmed lineages |
| minimum_lineage_abundance | float | no | 0.001 | Freyja --eps |
| coverage_estimate | int | no | 10 | 10x coverage estimate |
| timeseries_plot_interval | string | no | 0 | Timeseries interval (MS or D) |
| primer_version | string | no | — | Primer version |
| barcode_csv | string | no | — | Custom barcodes for demix |
| sample_metadata_csv | string | no | 0 | CSV with fastq→sampling date |
| keep_intermediates | boolean | no | true | Keep intermediates |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
All files from external wrapper (Freyja pipeline) — uploaded via `p3-cp --recursive` with suffix mapping.

#### Notes
Delegates to external wrapper via config.json. Same pattern as TaxonomicClassification v2.

---

### SequenceSubmission
- **Repo**: BV-BRC/bvbrc_sequence_submission
- **Description**: Sequence Submission to NCBI
- **Script**: App-SequenceSubmission

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_source | string | yes | — | enum: id_list, fasta_data, fasta_file, genome_group |
| input_fasta_data | string | no | — | FASTA input |
| input_fasta_file | string | no | — | Workspace FASTA file |
| input_genome_group | string | no | — | Workspace genome group |
| metadata | string | yes | — | Metadata CSV file (wsid) |
| affiliation | string | no | — | Submitter affiliation |
| first_name | string | yes | — | Submitter first name |
| last_name | string | yes | — | Submitter last name |
| email | string | yes | — | Submitter email |
| consortium | string | no | — | Consortium |
| country | string | no | — | Country |
| phoneNumber | string | no | — | Phone |
| street | string | no | — | Street |
| postal_code | string | no | — | Postal code |
| city | string | no | — | City |
| state | string | no | — | State |
| numberOfSequences | string | no | — | Number of sequences |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.csv` → csv
- `*.fasta` → contigs
- `*.fsa` → contigs
- `*.src` → csv
- `*.xml` → xml

#### Notes
Uses `p3-cp --recursive` with suffix mapping.

---

### Sleep
- **Repo**: BV-BRC/bvbrc_standalone_apps
- **Description**: Sleep a bit
- **Script**: App-Sleep

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| sleep_time | int | no | 10 | Time to sleep (seconds) |

#### Concrete Outputs
None. `donot_create_result_folder=1`.

---

### SubspeciesClassification
- **Repo**: BV-BRC/bvbrc_subspecies_classification
- **Description**: Subspecies Classification
- **Script**: App-SubspeciesClassification

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_source | string | yes | — | enum: id_list, fasta_data, fasta_file, genome_group |
| input_fasta_data | string | no | — | FASTA input |
| input_fasta_file | string | no | — | Workspace FASTA file |
| input_genome_group | string | no | — | Workspace genome group |
| ref_msa_fasta | string | no | — | Reference MSA (FASTA) |
| virus_type | string | yes | — | Virus type |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.tsv` → tsv (classification results)
- All files via `p3-cp -r` with suffix mapping (result→tsv)

---

### SyntenyGraph
- **Repo**: BV-BRC/bvbrc_synteny_graph
- **Description**: Compute synteny graph
- **Script**: App-SyntenyGraph

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| genome_ids | string[] | yes | [] | Input genomes |
| ksize | int | yes | 3 | Min neighborhood size |
| context | string | yes | genome | Alignment context |
| diversity | string | yes | species | Diversity quotient |
| alpha | string | yes | patric_pgfam | Alphabet for gene grouping |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `ps_graph.gexf` → gexf (synteny graph)
- `*.txt` → txt

---

### TaxonomicClassification (v1)
- **Repo**: BV-BRC/bvbrc_taxonomic_classification
- **Description**: Taxonomic classification for read data (Kraken2)
- **Script**: App-TaxonomicClassification

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_type | string | yes | — | enum: reads, contigs |
| contigs | File | no | — | Input contigs (wstype: Contigs) |
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, platform string[enum: infer/illumina/pacbio/nanopore], interleaved boolean, read_orientation_outward boolean, insert_size_mean int, insert_size_stdev float |
| single_end_libs | record[] | no | — | Fields: read File, platform string[enum: infer/illumina/pacbio/nanopore] |
| srr_ids | string[] | no | — | SRA Run IDs |
| save_classified_sequences | boolean | no | false | Save classified sequences |
| save_unclassified_sequences | boolean | no | false | Save unclassified sequences |
| algorithm | string | yes | Kraken2 | enum: Kraken2 |
| database | string | yes | Kraken2 | enum: Default NT, Kraken2, Greengenes, RDP, SILVA |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `full_report.txt` → txt (full Kraken2 report)
- `report.txt` → txt (filtered report)
- `output.txt` → txt (per-read classification)
- `output.txt.gz` → unspecified (compressed, if > 1MB)
- `chart.html` → html (Krona chart)
- `TaxonomicReport.html` → html (report)
- `classified*.fastq` → reads (if requested)
- `unclassified*.fastq` → reads (if requested)

---

### TaxonomicClassification (v2)
- **Repo**: BV-BRC/bvbrc_taxonomic_classification_2
- **Description**: Taxonomic classification v2 (multiple workflows)
- **Script**: App-TaxonomicClassification (same app ID)

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| host_genome | string | yes | no_host | enum: homo_sapiens, mus_musculus, rattus_norvegicus, caenorhabditis_elegans, drosophila_melanogaster_strain, danio_rerio_strain_tuebingen, gallus_gallus, macaca_mulatta, mustela_putorius_furo, sus_scrofa, no_host |
| analysis_type | string | yes | 16S | enum: pathogen, microbiome, 16S |
| paired_end_libs | record[] | no | — | Fields: sample_id string, read1 File, read2 File |
| single_end_libs | record[] | no | — | Fields: sample_id string, read File |
| srr_libs | record[] | no | — | Fields: sample_id string, srr_accession string |
| database | string | yes | SILVA | enum: bvbrc, Greengenes, SILVA, standard |
| save_classified_sequences | boolean | no | false | Save classified |
| save_unclassified_sequences | boolean | no | false | Save unclassified |
| confidence_interval | float | no | 0.1 | Confidence interval (0-1) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
All files from external wrapper — uploaded via `p3-cp --recursive` with suffix mapping.

#### Notes
Different architecture from v1. Delegates to external wrapper via config.json. Same app ID as v1.

---

### TnSeq
- **Repo**: BV-BRC/p3_tnseq
- **Description**: TnSeq analysis using TRANSIT
- **Script**: App-TnSeq

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| experimental_conditions | string[] | no | [] | Experimental conditions |
| contrasts | string[] | no | [] | Contrasts |
| read_files | record[] | no | — | Read file groups (fields not specified in spec) |
| reference_genome_id | string | yes | — | Reference genome ID |
| recipe | string | no | gumbel | enum: gumbel, griffin, tn5gaps, rankproduct, hmm, binomial, resampling |
| protocol | string | no | sassetti | enum: sassetti, tn5, mme1 |
| primer | string | no | "" | Primer DNA string for trimming |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.bam` → bam
- `*.bam.bai` → bam
- `*.counts` → txt
- `*.tn_stats` → txt
- `*.txt` → txt (including TRANSIT results)
- `*.wig` → wig

---

### TreeSort
- **Repo**: BV-BRC/bvbrc_TreeSort
- **Description**: Infer reassortment events along branches of a phylogenetic tree
- **Script**: App-TreeSort

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_source | string | yes | fasta_file_id | enum: fasta_data, fasta_existing_dataset, fasta_file_id, fasta_group_id |
| input_fasta_data | string | no | — | Input FASTA sequence |
| input_fasta_existing_dataset | string | no | — | Existing dataset directory |
| input_fasta_file_id | string | no | — | Workspace FASTA file ID |
| input_fasta_group_id | string | no | — | Workspace genome group ID |
| clades_path | string | no | — | Output file path for clades with reassortment |
| deviation | float | no | 2.0 | Max deviation from estimated substitution rate |
| equal_rates | boolean | no | — | Assume equal rates (no estimation) |
| inference_method | string | no | local | enum: local, mincut |
| match_regex | string | no | — | Custom regex to match segments |
| match_type | string | no | default | enum: default, epi, regex, strain |
| no_collapse | boolean | no | — | Do not collapse near-zero branches |
| p_value | float | no | 0.001 | P-value cutoff for reassortment tests |
| ref_segment | string | no | HA | Reference segment |
| ref_tree_inference | string | no | IQTree | enum: FastTree, IQTree |
| segments | string | no | — | Segments to analyze (empty=all) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.aln` → aligned_dna_fasta
- `*.csv` → csv (excluding descriptor.csv)
- `*.pdf` → pdf
- `*.xml` → phyloxml
- `*.tre` → unspecified
- `*.tsv` → tsv

#### Notes
Uses `p3-cp` with suffix mapping. External tool: `run_treesort`.

---

### Variation
- **Repo**: BV-BRC/bvbrc_variation
- **Description**: Identify and annotate small nucleotide variations vs. reference genome
- **Script**: App-Variation

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| reference_genome_id | string | yes | — | Reference genome ID |
| paired_end_libs | record[] | no | — | Fields: read1 File, read2 File, interleaved boolean, insert_size_mean int, insert_size_stdev float |
| single_end_libs | record[] | no | — | Fields: read File |
| srr_ids | string[] | no | — | SRA Run IDs |
| mapper | string | no | BWA-mem | enum: BWA-mem, BWA-mem-strict, Bowtie2, MOSAIK, LAST, minimap2, Snippy |
| caller | string | no | FreeBayes | enum: FreeBayes, BCFtools, Snippy |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.txt` → txt
- `*.tsv` → tsv (incl. var.annotated.raw.tsv)
- `*.vcf` → vcf (incl. var.snpEff.raw.vcf)
- `*.html` → html
- `*.bam` → bam
- `*.bigwig` → bigwig
- `*.consensus.fa` → contigs
- `*.tbi` → unspecified
- `*.vcf.gz` → unspecified
- `*.bam.bai` → unspecified
- `summary.txt` → txt
- `Text_Files_Circular_Viewer/` → folder (SNP effect visualization)

---

### ViralAssembly
- **Repo**: BV-BRC/bvbrc_viral_assembly
- **Description**: Assemble viral genomes
- **Script**: App-ViralAssembly

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| paired_end_lib | record | no | — | SINGULAR (no allow_multiple). Fields: read1 File, read2 File |
| single_end_lib | record | no | — | SINGULAR. Fields: read File |
| srr_id | string | no | — | SINGULAR SRA Run ID |
| strategy | string | no | auto | enum: auto, irma |
| module | string | no | — | Virus module; enum: FLU, CoV, RSV, EBOLA, FLU_AD, FLU-utr, FLU-minion |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
All files from work directory — recursively copied via `p3-cp -r -f` with suffix mapping (result→tsv).

#### Notes
SINGULAR parameter names: `paired_end_lib`, `single_end_lib`, `srr_id` (not plural).

---

### WholeGenomeSNPAnalysis
- **Repo**: BV-BRC/bvbrc_WholeGenomeSNPAnalysis
- **Description**: Identify SNP differences in a genome group
- **Script**: App-WholeGenomeSNPAnalysis

#### Inputs
| Parameter | CWL Type | Required | Default | Description |
|-----------|----------|----------|---------|-------------|
| input_genome_type | string | yes | — | enum: genome_group, genome_fasta |
| majority-threshold | float | no | 0.5 | Min fraction of genomes with locus (enum: 0-1 in 0.1 steps) |
| min_mid_linkage | int | no | 10 | Min mid linkage (max strong linkage) |
| max_mid_linkage | int | no | 40 | Max mid linkage (min weak linkage) |
| analysis_type | string | yes | — | enum: Whole Genome SNP Analysis |
| input_genome_group | string | no | — | Genome group name |
| input_genome_fasta | File | no | — | FASTA data (wstype: genome_fasta) |
| output_path | Directory | yes | — | Output folder |
| output_file | string | yes | — | File basename |

#### Concrete Outputs
- `*.txt` → txt
- `*.tsv` → tsv
- `*.phyloxml` → phyloxml
- `*.tre` → nwk
- `*.NJ` → txt
- `*.ML` → txt
- `*.5` → tsv (kSNP4)
- `*.vcf` → vcf
- `*.fasta` → contigs
- `*.html` → html (interactive SNP report)
- Subdirectories: Trees/, All_SNPs/, Core_SNPs/, VCFs/, Homoplasy/, Majority_SNPs/, report_supporting_documents/, Intermediate/

#### Notes
Uses `p3-cp --recursive` with suffix mapping. Hierarchical output directory structure.
