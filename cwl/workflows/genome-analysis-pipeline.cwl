cwlVersion: v1.2
class: Workflow
label: genome-analysis-pipeline

doc: |
  Genome Analysis Pipeline: CGA → CodonTree + CoreGenomeMLST + WholeGenomeSNPAnalysis

  Annotates one or more genomes from contigs using CGA (scatter), extracts
  genome_ids from workspace autometadata, creates a genome group combining
  the new genomes with user-provided reference genomes, then runs three
  comparative analyses in parallel.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

requirements:
  StepInputExpressionRequirement: {}
  InlineJavascriptRequirement: {}
  MultipleInputFeatureRequirement: {}
  ScatterFeatureRequirement: {}

inputs:
  # CGA inputs (contigs, scientific_name, taxonomy_id are parallel arrays for scatter)
  input_type:
    type: string
    doc: "Input type: reads, contigs, or genbank"
  contigs:
    type: string[]
    doc: "Workspace paths to input contigs"
  scientific_name:
    type: string[]
    doc: "Scientific name per genome"
  taxonomy_id:
    type: int[]?
    doc: "NCBI Taxonomy ID per genome"
  code:
    type: string
    default: "11"
    doc: "Genetic code (11 or 4)"
  domain:
    type: string
    default: "Bacteria"
    doc: "Domain: Bacteria or Archaea"

  # Comparison inputs
  reference_genome_ids:
    type: string[]
    doc: "BV-BRC genome IDs of reference genomes for comparison"
  optional_genome_ids:
    type: string[]?
    doc: "Optional genomes for CodonTree (not penalized for missing/duplicated PGFams). Use this for freshly-annotated genomes whose PGFam coverage may be incomplete."

  # cgMLST-specific
  input_schema_selection:
    type: string
    doc: "Species schema for cgMLST analysis"

  # Common
  output_path:
    type: string
    doc: "BV-BRC workspace output folder path [bvbrc:folder]"
  output_file:
    type: string
    doc: "Job name prefix — each BV-BRC app gets a unique suffix [bvbrc:wsid]"

steps:
  cga:
    run: "gowe://ComprehensiveGenomeAnalysis"
    scatter: [contigs, scientific_name, taxonomy_id]
    scatterMethod: dotproduct
    in:
      input_type: input_type
      contigs: contigs
      scientific_name: scientific_name
      taxonomy_id: taxonomy_id
      code: code
      domain: domain
      output_path: output_path
      output_file:
        source: output_file
        valueFrom: $(self + "_" + inputs.scientific_name.replace(/ /g, "_"))
    out: [annotated_genome, full_report, result_folder]

  get_genome_id:
    run: "gowe://bvbrc-get-genome-id"
    scatter: genome_ws_path
    in:
      genome_ws_path:
        source: cga/annotated_genome
        valueFrom: $(self.location.replace('ws://', ''))
    out: [genome_id]

  # Block until PGFam features for each freshly-annotated genome are indexed
  # in solr. CodonTree's preflight rejects genomes that can't anchor PGFam
  # alignments (App-CodonTree.pl verify_genome_ids). PGFam assignment is an
  # async indexing step that completes minutes to ~1 hour after annotation.
  wait_for_pgfams:
    run: "gowe://bvbrc-wait-for-pgfams"
    scatter: genome_id
    in:
      genome_id: get_genome_id/genome_id
    out: [genome_id]

  create_genome_group:
    run: "gowe://bvbrc-create-genome-group"
    in:
      group_name:
        source: output_file
        valueFrom: $(self + "_group")
      workspace_path: output_path
      genome_ids:
        source: [get_genome_id/genome_id, reference_genome_ids]
        linkMerge: merge_flattened
        valueFrom: |
          ${
            var ids = [];
            if (Array.isArray(self)) {
              self.forEach(function(item) {
                if (Array.isArray(item)) {
                  item.forEach(function(id) { ids.push(id); });
                } else {
                  ids.push(item);
                }
              });
            }
            return ids;
          }
    out: [genome_group_path]

  codon_tree:
    run: "gowe://CodonTree"
    in:
      # Main genomes: freshly-annotated (now PGFam-ready via wait_for_pgfams)
      # plus user-supplied references. All are real leaves on the resulting tree.
      genome_ids:
        source: [wait_for_pgfams/genome_id, reference_genome_ids]
        linkMerge: merge_flattened
        valueFrom: |
          ${
            var ids = [];
            if (Array.isArray(self)) {
              self.forEach(function(item) {
                if (Array.isArray(item)) {
                  item.forEach(function(id) { ids.push(id); });
                } else if (item != null) {
                  ids.push(item);
                }
              });
            }
            return ids;
          }
      optional_genome_ids: optional_genome_ids
      output_path: output_path
      output_file:
        source: output_file
        valueFrom: $(self + "_tree")
    out: [tree_nwk, tree_phyloxml, report, result_folder]

  cgmlst:
    run: "gowe://CoreGenomeMLST"
    in:
      input_genome_type:
        default: "genome_group"
      input_genome_group: create_genome_group/genome_group_path
      input_schema_selection: input_schema_selection
      output_path: output_path
      output_file:
        source: output_file
        valueFrom: $(self + "_cgmlst")
    out: [report, allele_results, mstree_nwk, result_folder]

  snp_analysis:
    run: "gowe://WholeGenomeSNPAnalysis"
    in:
      input_genome_type:
        default: "genome_group"
      analysis_type:
        default: "Whole Genome SNP Analysis"
      input_genome_group: create_genome_group/genome_group_path
      output_path: output_path
      output_file:
        source: output_file
        valueFrom: $(self + "_snp")
    out: [report, core_snps, all_snps, result_folder]

outputs:
  cga_reports:
    type: File[]
    outputSource: cga/full_report
  cga_genomes:
    type: File[]
    outputSource: cga/annotated_genome
  genome_ids:
    type: string[]
    outputSource: get_genome_id/genome_id
  genome_group:
    type: string
    outputSource: create_genome_group/genome_group_path
  phylo_tree:
    type: File
    outputSource: codon_tree/tree_nwk
  phylo_report:
    type: File
    outputSource: codon_tree/report
  cgmlst_report:
    type: File
    outputSource: cgmlst/report
  cgmlst_alleles:
    type: File
    outputSource: cgmlst/allele_results
  snp_report:
    type: File
    outputSource: snp_analysis/report
  snp_core:
    type: File
    outputSource: snp_analysis/core_snps
