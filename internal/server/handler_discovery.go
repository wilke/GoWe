package server

import "net/http"

type endpointInfo struct {
	Path        string   `json:"path"`
	Methods     []string `json:"methods"`
	Description string   `json:"description"`
}

type discoveryResponse struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Endpoints   []endpointInfo `json:"endpoints"`
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondOK(w, reqID, discoveryResponse{
		Name:        "GoWe API",
		Version:     "v1",
		Description: "GoWe Workflow Engine — CWL-based workflow submission, scheduling, and management",
		Endpoints: []endpointInfo{
			{"/api/v1/workflows", []string{"GET", "POST"}, "Workflow definition management"},
			{"/api/v1/workflows/{id}", []string{"GET", "PUT", "DELETE"}, "Single Workflow operations"},
			{"/api/v1/workflows/{id}/validate", []string{"POST"}, "Validate a Workflow without persisting"},
			{"/api/v1/submissions", []string{"GET", "POST"}, "Submission (run) management. POST accepts ?dry_run=true for validation without execution"},
			{"/api/v1/submissions/{id}", []string{"GET"}, "Single Submission detail with Tasks"},
			{"/api/v1/submissions/{id}/cancel", []string{"PUT"}, "Cancel a running Submission"},
			{"/api/v1/submissions/{sid}/tasks", []string{"GET"}, "List Tasks in a Submission"},
			{"/api/v1/submissions/{sid}/tasks/{tid}", []string{"GET"}, "Single Task detail"},
			{"/api/v1/submissions/{sid}/tasks/{tid}/logs", []string{"GET"}, "Task stdout/stderr logs"},
			{"/api/v1/apps", []string{"GET"}, "List available BV-BRC applications (cached)"},
			{"/api/v1/apps/{app_id}", []string{"GET"}, "Get BV-BRC app parameter schema"},
			{"/api/v1/apps/{app_id}/cwl-tool", []string{"GET"}, "Auto-generated CWL tool wrapper from app schema"},
			{"/api/v1/workspace", []string{"GET"}, "Browse BV-BRC workspace contents (proxy)"},
			{"/api/v1/health", []string{"GET"}, "Server health and version"},
		},
	})
}
