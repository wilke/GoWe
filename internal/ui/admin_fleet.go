package ui

import (
	"net/http"

	"github.com/me/gowe/pkg/model"
)

// HandleAdminFleet renders the complete worker fleet for admins.
//
// This deliberately calls store.ListWorkers directly rather than going
// through the paginated /api/v1/workers endpoint (default page size 20):
// an admin auditing the fleet needs every row, not just the first page.
// store.ListWorkers has no LIMIT clause, so it always returns the full set.
func (ui *UI) HandleAdminFleet(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	workers, err := ui.store.ListWorkers(r.Context())
	if err != nil {
		ui.renderError(w, "Failed to load worker fleet", err)
		return
	}

	// Resolve the submission behind each worker's current task, for linking.
	taskSubmission := map[string]string{}
	for _, wk := range workers {
		if wk.CurrentTask != "" {
			if t, err := ui.store.GetTask(r.Context(), wk.CurrentTask); err == nil && t != nil {
				taskSubmission[wk.CurrentTask] = t.SubmissionID
			}
		}
	}

	var online, offline, draining, gpu int
	for _, wk := range workers {
		switch wk.State {
		case model.WorkerStateOnline:
			online++
		case model.WorkerStateOffline:
			offline++
		case model.WorkerStateDraining:
			draining++
		}
		if wk.GPUEnabled {
			gpu++
		}
	}

	data := map[string]any{
		"Title":          "Worker Fleet - GoWe",
		"Session":        sess,
		"Workers":        workers,
		"Total":          len(workers),
		"OnlineCount":    online,
		"OfflineCount":   offline,
		"DrainingCount":  draining,
		"GPUCount":       gpu,
		"TaskSubmission": taskSubmission,
	}
	ui.render(w, "admin/fleet", data)
}
