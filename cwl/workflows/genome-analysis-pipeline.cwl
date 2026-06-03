cwlVersion: v1.2
class: Workflow

doc: |
  Genome Analysis Pipeline: CGA → PhylogeneticTree + CoreGenomeMLST + WholeGenomeSNPAnalysis

  Annotates a genome from reads or contigs using CGA, extracts the genome_id
  from workspace autometadata, creates a genome group combining the new genome
  with user-provided reference genomes, then runs three comparative analyses
  in parallel.

$namespaces:
  gowe: "https://github.com/wilke/GoWe#"

requirements:
  StepInputExpressionRequirement: {}
  InlineJavascriptRequirement: {}
  MultipleInputFeatureRequirement: {}

inputs:
  # CGA inputs
  input_type:
    type: string
    doc: "Input type: reads, contigs, or genbank"
  contigs:
    type: File?
    doc: "Input contigs (when input_type=contigs)"
  scientific_name:
    type: string
    doc: "Scientific name of genome to annotate"
  taxonomy_id:
    type: int?
    doc: "NCBI Taxonomy ID"
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
  out_genome_ids:
    type: string[]?
    doc: "Outgroup genome IDs for phylogenetic tree"

  # cgMLST-specific
  input_schema_selection:
    type: string
    doc: "Species schema for cgMLST analysis"

  # Common
  output_path:
    type: Directory
    doc: "BV-BRC workspace output folder [bvbrc:folder]"
  output_file:
    type: string
    doc: "Basename for output files [bvbrc:wsid]"

steps:
  cga:
    run: "gowe://ComprehensiveGenomeAnalysis"
    in:
      input_type: input_type
      contigs: contigs
      scientific_name: scientific_name
      taxonomy_id: taxonomy_id
      code: code
      domain: domain
      output_path: output_path
      output_file: output_file
    out: [annotated_genome, full_report, result_folder]

  get_genome_id:
    run: "gowe://bvbrc-get-genome-id"
    in:
      genome_ws_path:
        source: cga/annotated_genome
        valueFrom: $(self.location.replace('ws://', ''))
    out: [genome_id]

  create_genome_group:
    run: "gowe://bvbrc-create-genome-group"
    in:
      group_name:
        source: output_file
        valueFrom: $(self + "_comparison_group")
      workspace_path:
        source: output_path
        valueFrom: $(self.location.replace('ws://', ''))
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

  phylo_tree:
    run: "gowe://PhylogeneticTree"
    in:
      in_genome_ids:
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
      out_genome_ids: out_genome_ids
      output_path: output_path
      output_file:
        source: output_file
        valueFrom: $(self + "_phylo")
      full_tree_method:
        default: "ml"
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
  cga_report:
    type: File
    outputSource: cga/full_report
  cga_genome:
    type: File
    outputSource: cga/annotated_genome
  genome_id:
    type: string
    outputSource: get_genome_id/genome_id
  genome_group:
    type: string
    outputSource: create_genome_group/genome_group_path
  phylo_tree:
    type: File
    outputSource: phylo_tree/tree_nwk
  phylo_report:
    type: File
    outputSource: phylo_tree/report
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
