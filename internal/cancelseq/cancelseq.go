// Package cancelseq holds the submission-cancel sequence shared by the API
// and web UI cancel handlers.
//
// Before this package existed, the UI cancel handler (internal/ui) finalized
// a submission by writing its state directly and returning — it never
// called CancelNonTerminalSteps/Tasks and never fanned out to sub-workflow
// child submissions, so a UI-initiated cancel left tasks running and workers
// never learned about it (issue #185). The API cancel handler
// (internal/server) already did this correctly, but internal/server imports
// internal/ui (for its embedded web UI), so internal/ui cannot import
// internal/server back without a cycle — neither handler's package is a
// valid seam. This package is: both internal/server and internal/ui import
// it, it imports neither, and it depends only on internal/store,
// internal/metrics, and pkg/model.
//
// State validation stays with each caller (the API returns 409 via
// CanTransitionTo, the UI returns 400 via IsTerminal) since the two surfaces
// report failures differently; Run assumes sub already carries
// State=CANCELLED and CompletedAt set.
package cancelseq

import (
	"context"
	"log/slog"
	"time"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Result reports what a cancel sequence actually did, for the caller's
// response envelope.
type Result struct {
	StepsCancelled    int
	TasksCancelled    int
	ChildrenCancelled int
}

// Run performs the full submission-cancel sequence: persists sub (which the
// caller has already moved to State=CANCELLED with CompletedAt set),
// cancels its non-terminal steps/tasks, synchronously fans out to
// descendant sub-workflow child submissions at any nesting depth, and
// observes gowe_submission_wall_seconds{outcome="cancelled"} and
// gowe_tasks_skipped_total for every cancellation this call performs. reg
// may be nil (metrics disabled) — every Registry method no-ops on a nil
// receiver.
func Run(ctx context.Context, st store.Store, reg *metrics.Registry, logger *slog.Logger, sub *model.Submission, now time.Time) (Result, error) {
	if err := st.UpdateSubmission(ctx, sub); err != nil {
		return Result{}, err
	}
	reg.ObserveSubmissionWall(sub)

	stepsCancelled, err := st.CancelNonTerminalSteps(ctx, sub.ID, now)
	if err != nil {
		logger.Error("cancel non-terminal steps", "submission_id", sub.ID, "error", err)
	}

	tasksCancelled, err := st.CancelNonTerminalTasks(ctx, sub.ID, now)
	if err != nil {
		logger.Error("cancel non-terminal tasks", "submission_id", sub.ID, "error", err)
	}
	reg.AddTasksSkipped(tasksCancelled)

	childrenCancelled := CancelDescendants(ctx, st, reg, logger, sub.ID, now, map[string]bool{})

	return Result{
		StepsCancelled:    stepsCancelled,
		TasksCancelled:    tasksCancelled,
		ChildrenCancelled: childrenCancelled,
	}, nil
}

// CancelDescendants walks submissionID's subworkflow proxy tasks and
// synchronously cancels every non-terminal descendant child submission (any
// nesting depth), then retires the proxies as SKIPPED. It returns the number
// of child submissions this call actually transitioned to CANCELLED.
//
// This mirrors the scheduler's per-tick cancelChildSubmission cascade but
// runs it to full depth in one pass; the scheduler's reconciliation remains
// the backstop for cancels arriving via other paths (a crash mid-fan-out, or
// a submission cancelled by a path that doesn't call this package). All
// writes are CAS (FinalizeSubmission/TerminalizeTask skip already-terminal
// rows; CancelNonTerminalSteps/Tasks only touch non-terminal rows), so
// racing the scheduler is harmless in both directions. The visited set
// guards against cycles.
//
// Exported so both Run and existing server-side test helpers (which predate
// this package) can call it directly.
func CancelDescendants(ctx context.Context, st store.Store, reg *metrics.Registry, logger *slog.Logger, submissionID string, now time.Time, visited map[string]bool) int {
	if visited[submissionID] {
		return 0
	}
	visited[submissionID] = true

	tasks, err := st.ListTasksBySubmission(ctx, submissionID)
	if err != nil {
		logger.Error("cancel fan-out: list tasks", "submission_id", submissionID, "error", err)
		return 0
	}

	cancelled := 0
	for _, task := range tasks {
		if task.ExecutorType != model.ExecutorTypeSubworkflow {
			continue
		}
		children, err := st.GetChildSubmissions(ctx, task.ID)
		if err != nil {
			logger.Error("cancel fan-out: list children", "task_id", task.ID, "error", err)
			continue
		}
		// A childless proxy is mid-dispatch: the scheduler may create the
		// child at any moment. Leave the proxy RUNNING — retiring it here
		// would disarm pollSubworkflowTask's reconciliation, the only path
		// that cancels a child created after this walk.
		if len(children) == 0 {
			continue
		}
		// If any child could not be verifiably cancelled, keep the proxy
		// RUNNING so the scheduler's reconciliation stays armed as backstop.
		allHandled := true
		for _, child := range children {
			if !child.State.IsTerminal() {
				child.State = model.SubmissionStateCancelled
				child.CompletedAt = &now
				applied, err := st.FinalizeSubmission(ctx, child)
				if err != nil {
					logger.Error("cancel fan-out: finalize child", "child_id", child.ID, "error", err)
					allHandled = false
					continue
				}
				if applied {
					cancelled++
					reg.ObserveSubmissionWall(child)
					logger.Info("child submission cancelled (fan-out)", "child_id", child.ID)
				} else if fresh, err := st.GetSubmission(ctx, child.ID); err == nil && fresh != nil {
					// A concurrent writer (scheduler cascade or completion)
					// terminalized the child first — leave its state alone.
					logger.Debug("cancel fan-out: child already terminal",
						"child_id", child.ID, "state", fresh.State)
				}
			}
			// Steps/tasks: these only touch non-terminal rows, so they are
			// safe (and useful) regardless of who terminalized the child.
			if _, err := st.CancelNonTerminalSteps(ctx, child.ID, now); err != nil {
				logger.Error("cancel fan-out: cancel child steps", "child_id", child.ID, "error", err)
			}
			childTasksCancelled, err := st.CancelNonTerminalTasks(ctx, child.ID, now)
			if err != nil {
				logger.Error("cancel fan-out: cancel child tasks", "child_id", child.ID, "error", err)
			}
			reg.AddTasksSkipped(childTasksCancelled)

			// Recurse even into already-terminal children: grandchildren may
			// still be active (e.g. a scheduler cascade cancelled the child
			// but has not reached the next nesting level yet).
			cancelled += CancelDescendants(ctx, st, reg, logger, child.ID, now, visited)
		}
		if !allHandled {
			continue
		}
		// Every child verifiably terminal — retire the proxy as SKIPPED.
		// CAS: an already-terminal proxy (concurrent scheduler write) stays.
		// Proxies are excluded from CancelNonTerminalTasks (so its RETURN
		// COUNT never sees them) — this is the one per-row SKIP count,
		// gated on the CAS write actually applying.
		task.State = model.TaskStateSkipped
		task.CompletedAt = &now
		if applied, err := st.TerminalizeTask(ctx, task); err != nil {
			logger.Error("cancel fan-out: skip proxy task", "task_id", task.ID, "error", err)
		} else if applied {
			reg.AddTasksSkipped(1)
		}
	}
	return cancelled
}
