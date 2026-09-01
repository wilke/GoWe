package server

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// Task kinds reported by the timing view.
const (
	timingKindTask             = "task"
	timingKindSubworkflow      = "subworkflow"
	timingKindSkippedIteration = "skipped-iteration"
	timingKindCancelled        = "cancelled"
)

// maxTimingDepth bounds ?include_children recursion (defense-in-depth beside
// the visited set — the parent/child graph is a tree by construction).
const maxTimingDepth = 16

// taskTiming is the timing projection of one task row. It deliberately
// carries timing fields and raw timestamps ONLY — task rows also hold
// Tool/Job/stdout/stderr, which must never ship through this endpoint. [S4]
type taskTiming struct {
	TaskID       string `json:"task_id"`
	StepID       string `json:"step_id"`
	ScatterIndex int    `json:"scatter_index"`
	Executor     string `json:"executor"`
	WorkerGroup  string `json:"worker_group,omitempty"`
	State        string `json:"state"`
	// Kind classifies the row: "task", "subworkflow" (proxy paired with a
	// child submission), "skipped-iteration" (when-skip synthetic: SUCCESS
	// with no started_at, excluded from every duration aggregate), or
	// "cancelled" (SKIPPED).
	Kind string `json:"kind"`
	// QueueS is the time spent waiting before execution. For non-terminal
	// tasks it is "waiting so far" (now − created_at); for RETRYING/SCHEDULED
	// it is measured since first dispatch and therefore includes prior
	// attempts' run time.
	QueueS *float64 `json:"queue_s,omitempty"`
	// RunS is the execution duration. Present only when both timestamps
	// exist and the state permits trusting them (SUCCESS/FAILED, or the last
	// failed attempt's window on RETRYING/SCHEDULED, flagged retrying). For
	// "subworkflow" rows it is the child submission's wall time once the
	// child is terminal (the proxy's own stamps lag the child by up to one
	// scheduler tick). [S5]
	RunS *float64 `json:"run_s,omitempty"`
	// Retrying marks rows between attempts (RETRYING/SCHEDULED): their
	// timestamps describe the last failed attempt, not a final result.
	Retrying          bool       `json:"retrying,omitempty"`
	RetryCount        int        `json:"retry_count"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	ChildSubmissionID string     `json:"child_submission_id,omitempty"`
}

// stepTiming aggregates one step instance's tasks.
type stepTiming struct {
	StepID string `json:"step_id"`
	State  string `json:"state"`
	// WallS spans min(task.created_at) → step completion (now for in-flight
	// steps). For inline (zero-task) steps it spans si.created → si.completed
	// and therefore includes dependency wait — si.created is submission time.
	WallS *float64 `json:"wall_s,omitempty"`
	// FanInS is the gap between the last task completing and the step
	// instance completing (scatter merge / output collection).
	FanInS  *float64 `json:"fan_in_s,omitempty"`
	MaxRunS *float64 `json:"max_run_s,omitempty"`
	Tasks   int      `json:"tasks"`
	// Inline marks zero-task steps (ExpressionTool inline evaluation,
	// when-skipped step instances).
	Inline      bool       `json:"inline,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// submissionTiming carries the submission-level aggregates.
type submissionTiming struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// WallS is created → completed (or "so far" for in-flight submissions).
	WallS *float64 `json:"wall_s,omitempty"`
	// SchedulingS is submission creation → first task creation (pre-dispatch
	// work incl. prestage); absent until the first task exists.
	SchedulingS *float64 `json:"scheduling_s,omitempty"`
	// ComputeS sums run_s across tasks (subworkflow proxies contribute their
	// child's wall time); QueueS sums queue_s. Skipped iterations are
	// excluded from both.
	ComputeS float64 `json:"compute_s"`
	QueueS   float64 `json:"queue_s"`
	// CriticalPathS is the longest path over the workflow step DAG, summing
	// each step's [min task created (or si.created for inline), si.completed]
	// interval along dependency chains. Absent when the workflow definition
	// is unavailable.
	CriticalPathS *float64          `json:"critical_path_s,omitempty"`
	Counts        model.TaskSummary `json:"counts"`
	CreatedAt     time.Time         `json:"created_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

// timingReport is the /timing response body.
type timingReport struct {
	Submission submissionTiming `json:"submission"`
	Steps      []stepTiming     `json:"steps"`
	Tasks      []taskTiming     `json:"tasks"`
	Children   []*timingReport  `json:"children,omitempty"`
}

// handleSubmissionTiming serves GET /api/v1/submissions/{id}/timing: a
// timing-only projection of a submission's steps and tasks. With
// ?include_children=true it recurses into sub-workflow child submissions.
func (s *Server) handleSubmissionTiming(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	sub, err := s.store.GetSubmission(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", id))
		return
	}

	// Ownership check: non-admin users can only view their own submissions.
	userCtx := UserFromContext(r.Context())
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	includeChildren := r.URL.Query().Get("include_children") == "true"
	now := time.Now().UTC()
	report, err := s.buildTimingReport(r.Context(), sub, now, includeChildren, map[string]bool{}, 0)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondOK(w, reqID, report)
}

// buildTimingReport assembles the timing view for one submission. The same
// `now` flows through the whole (possibly recursive) report so "so far"
// durations are mutually consistent.
func (s *Server) buildTimingReport(ctx context.Context, sub *model.Submission, now time.Time,
	includeChildren bool, visited map[string]bool, depth int) (*timingReport, error) {

	visited[sub.ID] = true

	tasks, err := s.store.ListTasksBySubmission(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	steps, err := s.store.ListStepsBySubmission(ctx, sub.ID)
	if err != nil {
		return nil, err
	}

	// The workflow definition is only needed for the critical path; a
	// missing/deleted workflow degrades to critical_path_s = null.
	var wf *model.Workflow
	if sub.WorkflowID != "" {
		wf, err = s.store.GetWorkflow(ctx, sub.WorkflowID)
		if err != nil {
			s.logger.Warn("timing: workflow unavailable, critical path omitted",
				"submission_id", sub.ID, "workflow_id", sub.WorkflowID, "error", err)
			wf = nil
		}
	}

	// Resolve child submissions for sub-workflow proxies. When-skip
	// synthetics (SUCCESS with no started_at) never have children — skip the
	// lookup.
	childByTask := map[string]*model.Submission{}
	for _, t := range tasks {
		if t.ExecutorType != model.ExecutorTypeSubworkflow {
			continue
		}
		if t.State == model.TaskStateSuccess && t.StartedAt == nil {
			continue
		}
		kids, err := s.store.GetChildSubmissions(ctx, t.ID)
		if err != nil {
			s.logger.Warn("timing: child submission lookup failed",
				"task_id", t.ID, "error", err)
			continue
		}
		if len(kids) > 0 {
			childByTask[t.ID] = kids[0]
		}
	}

	report := &timingReport{
		Steps: []stepTiming{},
		Tasks: make([]taskTiming, 0, len(tasks)),
	}
	for _, t := range tasks {
		report.Tasks = append(report.Tasks, taskTimingRow(t, childByTask[t.ID], now))
	}

	// Group tasks per step instance. Tasks normally carry StepInstanceID;
	// legacy rows without one fall back to StepID matching.
	rowsBySI := map[string][]taskTiming{}
	for i, t := range tasks {
		key := t.StepInstanceID
		if key == "" {
			key = "step:" + t.StepID
		}
		rowsBySI[key] = append(rowsBySI[key], report.Tasks[i])
	}
	stepDurations := map[string]float64{}
	for _, si := range steps {
		rows := rowsBySI[si.ID]
		if len(rows) == 0 {
			rows = rowsBySI["step:"+si.StepID]
		}
		st, dur := buildStepTiming(si, rows, now)
		report.Steps = append(report.Steps, st)
		stepDurations[si.StepID] = dur
	}

	report.Submission = buildSubmissionTiming(sub, report.Tasks, now)
	report.Submission.CriticalPathS = criticalPathSeconds(wf, stepDurations)

	if includeChildren && depth < maxTimingDepth {
		for _, t := range tasks {
			child := childByTask[t.ID]
			if child == nil || visited[child.ID] {
				continue
			}
			childReport, err := s.buildTimingReport(ctx, child, now, true, visited, depth+1)
			if err != nil {
				return nil, err
			}
			report.Children = append(report.Children, childReport)
		}
	}

	return report, nil
}

// taskTimingRow applies the per-state trust rules to one task.
//
// Normative rules (plan #184 M2/N2):
//   - started_at PRESENCE may be checked in any state, but it is trusted as a
//     timestamp only in RUNNING/SUCCESS/FAILED. In particular a QUEUED worker
//     task's started_at is a stale dispatch stamp and is never used.
//   - run_s requires both stamps AND state SUCCESS/FAILED — except
//     RETRYING/SCHEDULED, which report the last failed attempt's window
//     flagged retrying:true.
//   - SUCCESS with no started_at is a when-skip synthetic iteration: kind
//     "skipped-iteration", excluded from all aggregates.
//   - SKIPPED (kind "cancelled") never has run_s; queue_s only if it never
//     started.
func taskTimingRow(t *model.Task, child *model.Submission, now time.Time) taskTiming {
	row := taskTiming{
		TaskID:       t.ID,
		StepID:       t.StepID,
		ScatterIndex: t.ScatterIndex,
		Executor:     string(t.ExecutorType),
		State:        string(t.State),
		Kind:         timingKindTask,
		RetryCount:   t.RetryCount,
		CreatedAt:    t.CreatedAt,
		StartedAt:    t.StartedAt,
		CompletedAt:  t.CompletedAt,
	}
	if t.RuntimeHints != nil {
		row.WorkerGroup = t.RuntimeHints.WorkerGroup
	}
	if child != nil {
		row.ChildSubmissionID = child.ID
	}

	// Kind classification. Order matters: when-skip synthetics ARE
	// ExecutorTypeSubworkflow rows, so the nil-started SUCCESS check runs
	// first; a SKIPPED proxy reads as "cancelled" (state wins) but keeps its
	// child_submission_id.
	switch {
	case t.State == model.TaskStateSuccess && t.StartedAt == nil:
		row.Kind = timingKindSkippedIteration
		return row // Excluded from every duration aggregate.
	case t.State == model.TaskStateSkipped:
		row.Kind = timingKindCancelled
	case t.ExecutorType == model.ExecutorTypeSubworkflow:
		row.Kind = timingKindSubworkflow
	}

	switch t.State {
	case model.TaskStateRunning:
		if t.StartedAt != nil {
			row.QueueS = ptrSecs(t.StartedAt.Sub(t.CreatedAt))
		} else {
			row.QueueS = ptrSecs(now.Sub(t.CreatedAt))
		}
	case model.TaskStateSuccess, model.TaskStateFailed:
		if t.StartedAt != nil {
			row.QueueS = ptrSecs(t.StartedAt.Sub(t.CreatedAt))
			if t.CompletedAt != nil {
				row.RunS = ptrSecs(t.CompletedAt.Sub(*t.StartedAt))
			}
		} else if t.CompletedAt != nil {
			// FAILED before it ever started (e.g. no executor at submit):
			// the whole window was spent waiting.
			row.QueueS = ptrSecs(t.CompletedAt.Sub(t.CreatedAt))
		}
	case model.TaskStateSkipped:
		if t.StartedAt == nil && t.CompletedAt != nil {
			row.QueueS = ptrSecs(t.CompletedAt.Sub(t.CreatedAt))
		}
	case model.TaskStateRetrying, model.TaskStateScheduled:
		row.Retrying = true
		// Since first dispatch; includes prior attempts' run time.
		row.QueueS = ptrSecs(now.Sub(t.CreatedAt))
		if t.StartedAt != nil && t.CompletedAt != nil {
			// Last failed attempt's window (stamps persist until resubmit).
			row.RunS = ptrSecs(t.CompletedAt.Sub(*t.StartedAt))
		}
	default:
		// PENDING/QUEUED: waiting so far. A QUEUED worker task may carry a
		// stale dispatch-time started_at — never trust it here.
		row.QueueS = ptrSecs(now.Sub(t.CreatedAt))
	}

	// Sub-workflow proxies: run_s is the child's wall time once the child is
	// terminal. The proxy's own stamps lag the child by up to one scheduler
	// tick, so the child is authoritative. [S5]
	if row.Kind == timingKindSubworkflow && child != nil && child.State.IsTerminal() && child.CompletedAt != nil {
		row.RunS = ptrSecs(child.CompletedAt.Sub(child.CreatedAt))
	}

	return row
}

// buildStepTiming aggregates one step instance's task rows and returns the
// step's critical-path interval length in seconds.
func buildStepTiming(si *model.StepInstance, rows []taskTiming, now time.Time) (stepTiming, float64) {
	st := stepTiming{
		StepID:      si.StepID,
		State:       string(si.State),
		Tasks:       len(rows),
		Inline:      len(rows) == 0,
		CreatedAt:   si.CreatedAt,
		CompletedAt: si.CompletedAt,
	}

	end := now
	if si.CompletedAt != nil {
		end = *si.CompletedAt
	}

	if len(rows) == 0 {
		// Inline (zero-task) step: wall from the step instance's own stamps.
		// This includes dependency wait — si.created is submission time.
		if si.CompletedAt != nil {
			st.WallS = ptrSecs(si.CompletedAt.Sub(si.CreatedAt))
			return st, *st.WallS
		}
		// Not yet complete and no tasks: nothing to report, and the step
		// contributes no time to the critical path yet.
		return st, 0
	}

	minCreated := rows[0].CreatedAt
	var maxCompleted *time.Time
	for _, r := range rows {
		if r.CreatedAt.Before(minCreated) {
			minCreated = r.CreatedAt
		}
		if r.CompletedAt != nil && (maxCompleted == nil || r.CompletedAt.After(*maxCompleted)) {
			maxCompleted = r.CompletedAt
		}
		if r.Kind == timingKindSkippedIteration {
			continue // Excluded from max_run. [F-O]
		}
		if r.RunS != nil && (st.MaxRunS == nil || *r.RunS > *st.MaxRunS) {
			v := *r.RunS
			st.MaxRunS = &v
		}
	}

	st.WallS = ptrSecs(end.Sub(minCreated))
	if si.CompletedAt != nil && maxCompleted != nil {
		st.FanInS = ptrSecs(si.CompletedAt.Sub(*maxCompleted))
	}
	return st, *st.WallS
}

// buildSubmissionTiming computes the submission-level aggregates from the
// already-projected task rows.
func buildSubmissionTiming(sub *model.Submission, rows []taskTiming, now time.Time) submissionTiming {
	out := submissionTiming{
		ID:          sub.ID,
		State:       string(sub.State),
		CreatedAt:   sub.CreatedAt,
		CompletedAt: sub.CompletedAt,
	}

	end := now
	if sub.CompletedAt != nil {
		end = *sub.CompletedAt
	}
	out.WallS = ptrSecs(end.Sub(sub.CreatedAt))

	var firstTask *time.Time
	summaryStates := make([]model.Task, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		if firstTask == nil || r.CreatedAt.Before(*firstTask) {
			t := r.CreatedAt
			firstTask = &t
		}
		summaryStates = append(summaryStates, model.Task{State: model.TaskState(r.State)})
		if r.Kind == timingKindSkippedIteration {
			continue // Excluded from compute/queue sums. [F-O]
		}
		if r.RunS != nil {
			out.ComputeS += *r.RunS
		}
		if r.QueueS != nil {
			out.QueueS += *r.QueueS
		}
	}
	out.ComputeS = roundSecs(out.ComputeS)
	out.QueueS = roundSecs(out.QueueS)
	out.Counts = model.ComputeTaskSummary(summaryStates)
	if firstTask != nil {
		out.SchedulingS = ptrSecs(firstTask.Sub(sub.CreatedAt))
	}
	return out
}

// criticalPathSeconds computes the longest path over the workflow step DAG,
// summing per-step interval lengths along Step.DependsOn chains. Returns nil
// when the workflow definition is unavailable.
func criticalPathSeconds(wf *model.Workflow, stepDurations map[string]float64) *float64 {
	if wf == nil {
		return nil
	}

	deps := make(map[string][]string, len(wf.Steps))
	for _, step := range wf.Steps {
		deps[step.ID] = step.DependsOn
	}

	memo := map[string]float64{}
	var visit func(stepID string, onStack map[string]bool) float64
	visit = func(stepID string, onStack map[string]bool) float64 {
		if v, ok := memo[stepID]; ok {
			return v
		}
		if onStack[stepID] {
			return 0 // Cycle guard: parser-validated DAGs are acyclic, but never recurse forever.
		}
		onStack[stepID] = true
		defer delete(onStack, stepID)

		longestDep := 0.0
		for _, dep := range deps[stepID] {
			if d := visit(dep, onStack); d > longestDep {
				longestDep = d
			}
		}
		total := stepDurations[stepID] + longestDep
		memo[stepID] = total
		return total
	}

	longest := 0.0
	for _, step := range wf.Steps {
		if d := visit(step.ID, map[string]bool{}); d > longest {
			longest = d
		}
	}
	longest = roundSecs(longest)
	return &longest
}

// ptrSecs converts a duration to a millisecond-rounded seconds pointer,
// clamping negatives (cross-process clock skew) to zero.
func ptrSecs(d time.Duration) *float64 {
	if d < 0 {
		d = 0
	}
	v := roundSecs(d.Seconds())
	return &v
}

func roundSecs(v float64) float64 {
	return math.Round(v*1000) / 1000
}
