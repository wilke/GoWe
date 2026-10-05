# BV-BRC registered workflow specs

The CWL specs registered against the GoWe server that backs the BV-BRC
Copilot. These are the **source of truth** for what the Copilot's agents see
when they call `GET /api/v1/workflows/:id/inputs`.

Previously these lived unversioned in `Copilot/Agents/tmp_workflow_specs/`.

## Authoring rules

- The reference for what a BV-BRC app actually accepts is the website app
  form: `bvbrc_website/public/js/p3/widget/app/*.js`. The GoWe app catalog
  can lag behind it.
- Spec `doc` strings use the convention **"Required when selector=value"** so
  the LLM knows which payload input accompanies each selector enum value.
- After editing a spec, **re-register it on GoWe** and verify with
  `GET /api/v1/workflows/<id>/inputs`.

## Known-broken specs (2026-10-05)

Measured from the engine database — these have never produced a successful
run, and the cause is a spec/app contract mismatch, not an LLM error:

| Spec | Record | Symptom |
|---|---|---|
| `tmp_workflow_gene_tree.cwl` | 0 ok / 11 failed | `App-GeneTree.pl: Can't use string as a HASH ref` — `sequences` must be `{filename, type}` records |
| `tmp_workflow_msa_snp.cwl` | 1 ok / 11 failed | `App-MSA.pl: Can't use string as an ARRAY ref` — needs `feature_groups` / `select_genomegroup` arrays |
| `tmp_workflow_tree_sort.cwl` | 0 ok / 3 failed | never succeeded |
| `tmp_workflow_docking.cwl` | 0 ok / 2 failed | never succeeded |
| `tmp_workflow_homology.cwl` (BLAST) | 17 ok / 6 failed | needs `feature_group` input source, `blast_max_hits` default 10 |

See `Copilot/Agents/SYSTEM_NOTES.md` §4.2 and
`current_plans/chris_hackathon_feedback_fixes.md`.
