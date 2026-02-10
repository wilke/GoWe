package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

var cannedApps = []map[string]any{
	{
		"id":             "GenomeAssembly2",
		"label":          "Genome Assembly",
		"description":    "Assemble reads into contigs using SPAdes, MEGAHIT, or other assemblers",
		"default_cpu":    8,
		"default_memory": "128G",
	},
	{
		"id":             "GenomeAnnotation",
		"label":          "Genome Annotation",
		"description":    "Annotate a genome using RASTtk",
		"default_cpu":    4,
		"default_memory": "32G",
	},
	{
		"id":             "ComprehensiveGenomeAnalysis",
		"label":          "Comprehensive Genome Analysis",
		"description":    "Assembly + Annotation + Analysis pipeline",
		"default_cpu":    8,
		"default_memory": "128G",
	},
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondList(w, reqID, cannedApps, &model.Pagination{
		Total: len(cannedApps), Limit: 20, Offset: 0, HasMore: false,
	})
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	appID := chi.URLParam(r, "appID")

	for _, app := range cannedApps {
		if app["id"] == appID {
			// Return full schema for known apps
			if appID == "GenomeAssembly2" {
				app["parameters"] = []map[string]any{
					{"id": "paired_end_libs", "label": "Paired End Libraries", "type": "array", "required": false, "description": "List of paired-end read library objects"},
					{"id": "recipe", "label": "Assembly Recipe", "type": "enum", "required": true, "default": "auto", "enum": []string{"auto", "unicycler", "spades", "megahit", "velvet", "miniasm", "canu"}, "description": "Assembly algorithm to use"},
					{"id": "trim", "label": "Trim Reads", "type": "boolean", "required": false, "default": true, "description": "Trim adapters and low-quality bases before assembly"},
					{"id": "min_contig_len", "label": "Minimum Contig Length", "type": "int", "required": false, "default": 300, "description": "Minimum contig length to report"},
					{"id": "output_path", "label": "Output Path", "type": "string", "required": true, "description": "Workspace path for results"},
					{"id": "output_file", "label": "Output File Prefix", "type": "string", "required": true, "description": "Prefix for output file names"},
				}
			}
			respondOK(w, reqID, app)
			return
		}
	}

	respondError(w, reqID, http.StatusNotFound, &model.APIError{
		Code:    model.ErrNotFound,
		Message: "App '" + appID + "' not found in BV-BRC",
	})
}

func (s *Server) handleGetAppCWLTool(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	appID := chi.URLParam(r, "appID")

	// Check app exists
	found := false
	for _, app := range cannedApps {
		if app["id"] == appID {
			found = true
			break
		}
	}
	if !found {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    model.ErrNotFound,
			Message: "App '" + appID + "' not found in BV-BRC",
		})
		return
	}

	cwlTool := `cwlVersion: v1.2
class: CommandLineTool

hints:
  goweHint:
    bvbrc_app_id: ` + appID + `
    executor: bvbrc

baseCommand: ["true"]

doc: "Auto-generated CWL tool wrapper for ` + appID + `"

inputs:
  output_path:
    type: string
    doc: "Workspace path for results"
  output_file:
    type: string
    doc: "Prefix for output file names"

outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
`

	respondOK(w, reqID, map[string]any{
		"app_id":                       appID,
		"cwl_tool":                     cwlTool,
		"generated_from_schema_version": "2026-02-09T12:00:00Z",
		"output_registry_hit":          false,
	})
}
