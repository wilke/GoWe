package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

func cannedSubmissionDetail() map[string]any {
	return map[string]any{
		"id":            "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
		"workflow_id":   "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		"workflow_name": "assembly-annotation-pipeline",
		"state":         "COMPLETED",
		"inputs": map[string]any{
			"reads_r1":       map[string]any{"class": "File", "location": "/user@bvbrc/home/reads/sample1_R1.fastq.gz"},
			"reads_r2":       map[string]any{"class": "File", "location": "/user@bvbrc/home/reads/sample1_R2.fastq.gz"},
			"scientific_name": "Escherichia coli K-12",
			"taxonomy_id":    83333,
		},
		"outputs": map[string]any{
			"genome": map[string]any{"class": "File", "location": "/user@bvbrc/home/annotations/sample1/annotation.genome"},
		},
		"labels":       map[string]string{"project": "ecoli-analysis", "sample": "sample1"},
		"submitted_by": "user@bvbrc",
		"task_summary": map[string]any{"total": 2, "pending": 0, "running": 0, "success": 2, "failed": 0},
		"tasks":        cannedTasks(),
		"created_at":   "2026-02-09T17:35:00Z",
		"completed_at": "2026-02-09T17:40:00Z",
	}
}

func cannedDryRunReport() map[string]any {
	return map[string]any{
		"dry_run":      true,
		"valid":        true,
		"workflow":     map[string]any{"id": "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890", "name": "assembly-annotation-pipeline"},
		"inputs_valid": true,
		"steps": []map[string]any{
			{"id": "assemble", "executor_type": "bvbrc", "bvbrc_app_id": "GenomeAssembly2", "depends_on": []string{}, "app_schema_valid": true, "inputs_compatible": true},
			{"id": "annotate", "executor_type": "bvbrc", "bvbrc_app_id": "GenomeAnnotation", "depends_on": []string{"assemble"}, "app_schema_valid": true, "inputs_compatible": true},
		},
		"dag_acyclic":      true,
		"execution_order":  []string{"assemble", "annotate"},
		"executor_availability": map[string]string{"bvbrc": "available"},
		"errors":   []any{},
		"warnings": []any{},
	}
}

func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		WorkflowID string         `json:"workflow_id"`
		Inputs     map[string]any `json:"inputs"`
		Labels     map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	// Dry-run mode
	if r.URL.Query().Get("dry_run") == "true" {
		respondOK(w, reqID, cannedDryRunReport())
		return
	}

	id := "sub_" + uuid.New().String()
	respondCreated(w, reqID, map[string]any{
		"id":            id,
		"workflow_id":   req.WorkflowID,
		"workflow_name": "assembly-annotation-pipeline",
		"state":         "PENDING",
		"inputs":        req.Inputs,
		"labels":        req.Labels,
		"submitted_by":  "user@bvbrc",
		"task_summary":  map[string]any{"total": 2, "pending": 2, "running": 0, "success": 0, "failed": 0},
		"created_at":    "2026-02-09T17:35:00Z",
		"completed_at":  nil,
	})
}

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	data := []map[string]any{
		{
			"id":            "sub_c3d4e5f6-a7b8-9012-cdef-234567890abc",
			"workflow_id":   "wf_a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			"workflow_name": "assembly-annotation-pipeline",
			"state":         "COMPLETED",
			"labels":        map[string]string{"project": "ecoli-analysis"},
			"submitted_by":  "user@bvbrc",
			"task_summary":  map[string]any{"total": 2, "pending": 0, "running": 0, "success": 2, "failed": 0},
			"created_at":    "2026-02-09T17:35:00Z",
			"completed_at":  "2026-02-09T17:40:00Z",
		},
	}

	respondList(w, reqID, data, &model.Pagination{
		Total: 1, Limit: 20, Offset: 0, HasMore: false,
	})
}

func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondOK(w, reqID, cannedSubmissionDetail())
}

func (s *Server) handleCancelSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	respondOK(w, reqID, map[string]any{
		"id":                      id,
		"state":                   "CANCELLED",
		"tasks_cancelled":         1,
		"tasks_already_completed": 1,
	})
}
