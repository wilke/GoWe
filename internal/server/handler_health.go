package server

import (
	"context"
	"net/http"
	"runtime"
	"sort"
	"time"

	"github.com/me/gowe/pkg/model"
)

type workerSummary struct {
	Online   int      `json:"online"`
	Offline  int      `json:"offline"`
	Runtimes []string `json:"runtimes"`
	Groups   []string `json:"groups"`
}

type healthResponse struct {
	Status    string            `json:"status"`
	Version   string            `json:"version"`
	GoVersion string            `json:"go_version"`
	Uptime    string            `json:"uptime"`
	Scheduler string            `json:"scheduler"`
	Store     string            `json:"store"`
	Executors map[string]string `json:"executors"`
	Workers   *workerSummary    `json:"workers,omitempty"`
}

func (s *Server) executorStatus() map[string]string {
	status := make(map[string]string)
	for _, t := range []model.ExecutorType{
		model.ExecutorTypeLocal,
		model.ExecutorTypeContainer,
		model.ExecutorTypeBVBRC,
		model.ExecutorTypeWorker,
	} {
		if s.registry != nil && s.registry.Has(t) {
			status[string(t)] = "available"
		} else {
			status[string(t)] = "unavailable"
		}
	}
	// Apptainer registers as its own type but is functionally "container".
	if s.registry != nil && s.registry.Has(model.ExecutorTypeApptainer) {
		status["container"] = "available"
	}
	return status
}

func (s *Server) workerSummary() *workerSummary {
	workers, err := s.store.ListWorkers(context.Background())
	if err != nil {
		return nil
	}
	ws := &workerSummary{}
	runtimes := make(map[string]bool)
	groups := make(map[string]bool)
	for _, w := range workers {
		switch w.State {
		case model.WorkerStateOnline:
			ws.Online++
		default:
			ws.Offline++
		}
		for _, rt := range model.ParseRuntimes(w.Runtime) {
			if rt != model.RuntimeNone {
				runtimes[string(rt)] = true
			}
		}
		g := w.Group
		if g == "" {
			g = "default"
		}
		groups[g] = true
	}
	for rt := range runtimes {
		ws.Runtimes = append(ws.Runtimes, rt)
	}
	for g := range groups {
		ws.Groups = append(ws.Groups, g)
	}
	sort.Strings(ws.Runtimes)
	sort.Strings(ws.Groups)
	return ws
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
		Executors: s.executorStatus(),
		Workers:   s.workerSummary(),
	})
}
