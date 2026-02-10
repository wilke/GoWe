package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

func cannedTasks() []map[string]any {
	return []map[string]any{
		{
			"id":            "task_d4e5f6a7-b8c9-0123-def0-34567890abcd",
			"step_id":       "assemble",
			"state":         "SUCCESS",
			"executor_type": "bvbrc",
			"external_id":   "bvbrc-job-uuid-001",
			"bvbrc_app_id":  "GenomeAssembly2",
			"outputs": map[string]any{
				"contigs": map[string]any{"class": "File", "location": "/user@bvbrc/home/assemblies/sample1/sample1_assembly.contigs.fasta"},
			},
			"retry_count":  0,
			"created_at":   "2026-02-09T17:35:00Z",
			"started_at":   "2026-02-09T17:35:05Z",
			"completed_at": "2026-02-09T17:38:30Z",
		},
		{
			"id":            "task_e5f6a7b8-c9d0-1234-ef01-4567890abcde",
			"step_id":       "annotate",
			"state":         "SUCCESS",
			"executor_type": "bvbrc",
			"external_id":   "bvbrc-job-uuid-002",
			"bvbrc_app_id":  "GenomeAnnotation",
			"outputs": map[string]any{
				"annotated_genome": map[string]any{"class": "File", "location": "/user@bvbrc/home/annotations/sample1/annotation.genome"},
			},
			"retry_count":  0,
			"created_at":   "2026-02-09T17:38:31Z",
			"started_at":   "2026-02-09T17:38:35Z",
			"completed_at": "2026-02-09T17:40:00Z",
		},
	}
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondList(w, reqID, cannedTasks(), &model.Pagination{
		Total: 2, Limit: 20, Offset: 0, HasMore: false,
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	tasks := cannedTasks()
	tid := chi.URLParam(r, "tid")

	for _, t := range tasks {
		if t["id"] == tid {
			respondOK(w, reqID, t)
			return
		}
	}
	// Return first task as default for skeleton
	respondOK(w, reqID, tasks[0])
}

func (s *Server) handleGetTaskLogs(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	tid := chi.URLParam(r, "tid")
	respondOK(w, reqID, map[string]any{
		"task_id":   tid,
		"step_id":   "assemble",
		"stdout":    "SPAdes v3.15.5\nAssembling reads...\n=== Assembly complete ===\nContigs: 42\nTotal length: 4,641,652 bp\nN50: 245,312\n",
		"stderr":    "",
		"exit_code": 0,
	})
}
