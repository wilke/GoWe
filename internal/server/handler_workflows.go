package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// Skeleton canned workflow for development.
func cannedWorkflow(id, name string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	return map[string]any{
		"id":          id,
		"name":        name,
		"description": "Assemble reads then annotate the genome",
		"cwl_version": "v1.2",
		"inputs": []map[string]any{
			{"id": "reads_r1", "type": "File", "required": true},
			{"id": "reads_r2", "type": "File", "required": true},
			{"id": "scientific_name", "type": "string", "required": true},
			{"id": "taxonomy_id", "type": "int", "required": true},
		},
		"outputs": []map[string]any{
			{"id": "genome", "type": "File", "output_source": "annotate/annotated_genome"},
		},
		"steps": []map[string]any{
			{
				"id":         "assemble",
				"tool_ref":   "bvbrc-assembly.cwl",
				"depends_on": []string{},
				"in": []map[string]any{
					{"id": "read1", "source": "reads_r1"},
					{"id": "read2", "source": "reads_r2"},
				},
				"out": []string{"contigs"},
			},
			{
				"id":         "annotate",
				"tool_ref":   "bvbrc-annotation.cwl",
				"depends_on": []string{"assemble"},
				"in": []map[string]any{
					{"id": "contigs", "source": "assemble/contigs"},
					{"id": "scientific_name", "source": "scientific_name"},
					{"id": "taxonomy_id", "source": "taxonomy_id"},
				},
				"out": []string{"annotated_genome"},
			},
		},
		"created_at": now,
		"updated_at": now,
	}
}

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		CWL         string `json:"cwl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	name := req.Name
	if name == "" {
		name = "unnamed-workflow"
	}
	id := "wf_" + uuid.New().String()

	respondCreated(w, reqID, cannedWorkflow(id, name))
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	data := []map[string]any{
		{
			"id":          "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			"name":        "assembly-annotation-pipeline",
			"description": "Assemble reads then annotate the genome",
			"cwl_version": "v1.2",
			"step_count":  2,
			"created_at":  "2026-02-09T17:31:00Z",
		},
	}

	respondList(w, reqID, data, &model.Pagination{
		Total:   1,
		Limit:   20,
		Offset:  0,
		HasMore: false,
	})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	respondOK(w, reqID, cannedWorkflow(id, "assembly-annotation-pipeline"))
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	respondOK(w, reqID, cannedWorkflow(id, "assembly-annotation-pipeline"))
}

func (s *Server) handleDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondOK(w, reqID, map[string]any{"deleted": true})
}

func (s *Server) handleValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondOK(w, reqID, map[string]any{
		"valid":    true,
		"errors":   []any{},
		"warnings": []any{},
	})
}
