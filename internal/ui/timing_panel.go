package ui

import (
	"net/http"
	"time"

	"github.com/me/gowe/internal/timing"
)

// timingBarRow is the per-task view model for the timing panel's queue/run
// bars. Go templates can only do integer arithmetic, so the queue/run
// segment widths (percent of the widest row in the report) are precomputed
// here rather than in the template.
type timingBarRow struct {
	TaskID            string
	StepID            string
	Kind              string
	State             string
	ChildSubmissionID string
	QueueS            *float64
	RunS              *float64
	QueuePct          int
	RunPct            int
}

// buildTimingBars projects a timing report's task rows into bar view models,
// normalized against the longest single row (queue_s + run_s) in the report.
// Skipped-iteration rows (when-skip synthetics) are omitted — they carry no
// duration and would only clutter the panel.
func buildTimingBars(report *timing.Report) []timingBarRow {
	if report == nil {
		return nil
	}

	maxTotal := 0.0
	for _, t := range report.Tasks {
		if t.Kind == timing.KindSkippedIteration {
			continue
		}
		total := 0.0
		if t.QueueS != nil {
			total += *t.QueueS
		}
		if t.RunS != nil {
			total += *t.RunS
		}
		if total > maxTotal {
			maxTotal = total
		}
	}

	rows := make([]timingBarRow, 0, len(report.Tasks))
	for _, t := range report.Tasks {
		if t.Kind == timing.KindSkippedIteration {
			continue
		}
		row := timingBarRow{
			TaskID:            t.TaskID,
			StepID:            t.StepID,
			Kind:              t.Kind,
			State:             t.State,
			ChildSubmissionID: t.ChildSubmissionID,
			QueueS:            t.QueueS,
			RunS:              t.RunS,
		}
		if maxTotal > 0 {
			if t.QueueS != nil {
				row.QueuePct = int(*t.QueueS / maxTotal * 100)
			}
			if t.RunS != nil {
				row.RunPct = int(*t.RunS / maxTotal * 100)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// HandleSubmissionTimingPanel serves GET /submissions/{id}/timing-panel: the
// HTMX fragment backing the submission view's timing section. It is used
// both for the "include sub-workflow trees" toggle (?include_children=true)
// and, via the shared "timing_panel" component template, embedded directly
// in the initial page render for the common (no toggle) case.
//
// The computation is internal/timing.BuildReport — the exact function the
// JSON API's GET /api/v1/submissions/{id}/timing handler
// (internal/server/handler_timing.go) calls, so the panel can never drift
// from the API's numbers.
func (ui *UI) HandleSubmissionTimingPanel(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		ui.renderNotFound(w, "Submission not found")
		return
	}

	// Ownership check: non-admin users can only view their own submissions.
	if sess != nil && !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	includeChildren := r.URL.Query().Get("include_children") == "true"
	report, err := timing.BuildReport(r.Context(), ui.store, ui.logger, sub, time.Now().UTC(), includeChildren, map[string]bool{}, 0)
	if err != nil {
		ui.logger.Error("timing panel: build report failed", "submission_id", id, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	ui.renderFragment(w, "components/timing_panel", map[string]any{
		"SubmissionID":    id,
		"Report":          report,
		"IncludeChildren": includeChildren,
		"TimingBars":      buildTimingBars(report),
	})
}
