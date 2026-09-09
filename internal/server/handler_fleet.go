package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/me/gowe/pkg/model"
)

// FleetMember is a slim, read-only projection of model.Worker for the
// user-authenticated fleet roster. It deliberately omits Hostname and any
// dataset/secret-ish detail — this is a status view for any signed-in user,
// not the infra-level dump the admin/worker-key-authenticated GET /workers
// endpoint returns.
type FleetMember struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	State       model.WorkerState `json:"state"`
	Group       string            `json:"group"`
	Runtime     string            `json:"runtime"`
	Version     string            `json:"version,omitempty"`
	GPUEnabled  bool              `json:"gpu_enabled"`
	LastSeen    time.Time         `json:"last_seen"`
	CurrentTask string            `json:"current_task,omitempty"`
}

// FleetSummary is a small aggregate over the roster: total worker count and a
// breakdown by state (e.g. {"online": N, "offline": M}).
type FleetSummary struct {
	Total   int            `json:"total"`
	ByState map[string]int `json:"by_state"`
}

// fleetRosterData is the shape returned under the response envelope's "data" key.
type fleetRosterData struct {
	Members []FleetMember `json:"members"`
	Summary FleetSummary  `json:"summary"`
}

// handleFleetRoster returns a slim, read-only fleet status roster for any
// signed-in user — no worker key required. It sits behind apiAuthMiddleware
// (provider-token/user auth), the same group as /workflows and /submissions,
// so enabling --worker-keys (which gates GET /api/v1/workers) does not affect
// it. current_task may be stale (see #252 — out of scope here).
//
// GET /api/v1/fleet
func (s *Server) handleFleetRoster(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })

	members := make([]FleetMember, 0, len(workers))
	byState := make(map[string]int)
	for _, wk := range workers {
		members = append(members, FleetMember{
			ID:          wk.ID,
			Name:        wk.Name,
			State:       wk.State,
			Group:       wk.Group,
			Runtime:     string(wk.Runtime),
			Version:     wk.Version,
			GPUEnabled:  wk.GPUEnabled,
			LastSeen:    wk.LastSeen,
			CurrentTask: wk.CurrentTask,
		})
		byState[string(wk.State)]++
	}

	respondOK(w, reqID, fleetRosterData{
		Members: members,
		Summary: FleetSummary{
			Total:   len(members),
			ByState: byState,
		},
	})
}
