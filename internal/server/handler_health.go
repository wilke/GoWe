package server

import (
	"net/http"
	"runtime"
	"time"
)

type healthResponse struct {
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	GoVersion string            `json:"go_version"`
	Uptime    string            `json:"uptime"`
	Scheduler string            `json:"scheduler"`
	Store     string            `json:"store"`
	Executors map[string]string `json:"executors"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	respondOK(w, reqID, healthResponse{
		Status:    "healthy",
		Version:   "0.1.0",
		GoVersion: runtime.Version(),
		Uptime:    time.Since(s.startTime).Round(time.Second).String(),
		Scheduler: "not_started",
		Store:     "skeleton",
		Executors: map[string]string{
			"local":     "available",
			"bvbrc":     "unavailable",
			"container": "unavailable",
		},
	})
}
