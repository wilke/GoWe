package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/internal/cancelseq"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/pkg/model"
)

// submissionResponse wraps a submission with secrets_state, computed from
// SecretNames/SecretsPurgedAt (model.Submission.SecretsState() is a method,
// not a field, so it is invisible to plain json.Marshal). secret_names,
// secrets_retention, and secrets_purged_at already carry their own json
// tags on model.Submission and need no wrapping; Secrets itself is
// json:"-" and never round-trips through this or any other response type.
// Used everywhere a full submission is serialized: create, get, list.
type submissionResponse struct {
	*model.Submission
	SecretsState string `json:"secrets_state,omitempty"`
}

func newSubmissionResponse(sub *model.Submission) *submissionResponse {
	if sub == nil {
		return nil
	}
	sub = sanitizeSubmissionTasks(sub)
	return &submissionResponse{Submission: sub, SecretsState: sub.SecretsState()}
}

// sanitizeSubmissionTasks returns a copy of sub with its embedded Tasks
// (populated by GetSubmission, never by the list/create paths) sanitized via
// sanitizeTaskValueSlice — the same RuntimeHints.Secrets/HTTPCredential
// stripping applied to every other task-serializing surface (C1). Returns
// sub itself, unmodified, when there are no tasks to scrub. sub and its
// original Tasks slice are left untouched either way.
func sanitizeSubmissionTasks(sub *model.Submission) *model.Submission {
	if sub == nil || len(sub.Tasks) == 0 {
		return sub
	}
	out := *sub
	out.Tasks = sanitizeTaskValueSlice(sub.Tasks)
	return &out
}

func newSubmissionListResponse(subs []*model.Submission) []*submissionResponse {
	out := make([]*submissionResponse, len(subs))
	for i, sub := range subs {
		out[i] = newSubmissionResponse(sub)
	}
	return out
}

// requireSubmissionAccess checks whether the given user context has permission
// to access the submission. Admins and unauthenticated contexts (nil) are always
// allowed; regular users may only access their own submissions.
func requireSubmissionAccess(sub *model.Submission, userCtx *UserContext) bool {
	if userCtx == nil || userCtx.User.IsAdmin() {
		return true
	}
	return sub.SubmittedBy == userCtx.User.Username
}

func (s *Server) handleCreateSubmission(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	// Get authenticated user context.
	userCtx := UserFromContext(r.Context())
	if userCtx == nil {
		respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
			Code:    model.ErrUnauthorized,
			Message: "authentication required",
		})
		return
	}

	var req struct {
		WorkflowID        string            `json:"workflow_id"`
		Inputs            map[string]any    `json:"inputs"`
		Labels            map[string]string `json:"labels"`
		OutputDestination string            `json:"output_destination"`
		Secrets           map[string]string `json:"secrets"`
		SecretsRetention  string            `json:"secrets_retention"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "Invalid JSON body: " + err.Error(),
		})
		return
	}

	if req.WorkflowID == "" {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("missing required field",
				model.FieldError{Field: "workflow_id", Message: "workflow_id is required"}))
		return
	}

	// Resolve workflow: try by ID first, fall back to name lookup.
	wf, err := s.resolveWorkflow(r.Context(), req.WorkflowID)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}
	if wf == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("workflow", req.WorkflowID))
		return
	}

	// Check anonymous restrictions.
	if userCtx.User.IsAnonymous() && s.anonConfig != nil {
		if err := s.validateAnonymousSubmission(wf); err != nil {
			respondError(w, reqID, http.StatusForbidden, &model.APIError{
				Code:    model.ErrForbidden,
				Message: err.Error(),
			})
			return
		}
	}

	// Anonymous submissions never carry secrets. A submission's secrets are
	// scoped to its submitter (owner/admin-only purge, retention tied to
	// the submitting identity); the anonymous identity is shared by every
	// unauthenticated caller in --allow-anonymous deployments, so there is
	// no principal to scope that access to. Refuse up front — mirroring how
	// delegated provider tokens are handled, secrets are a credential and
	// anonymous submissions carry none today.
	if userCtx.User.IsAnonymous() {
		declaresSecretInput := false
		for _, id := range wf.SecretInputs {
			if _, ok := req.Inputs[id]; ok {
				declaresSecretInput = true
				break
			}
		}
		if len(req.Secrets) > 0 || declaresSecretInput {
			respondError(w, reqID, http.StatusForbidden, &model.APIError{
				Code:    model.ErrForbidden,
				Message: "anonymous submissions may not include secrets; authenticate first",
			})
			return
		}
	}

	// Dry-run: validate without creating a submission.
	if r.URL.Query().Get("dry_run") == "true" {
		respondOK(w, reqID, s.buildDryRunReport(wf, req.Inputs))
		return
	}

	if err := model.ValidateSecrets(req.Secrets); err != nil {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError(err.Error()))
		return
	}

	// secrets_retention: empty means "use the server default"
	// (--secrets-retention, see server.WithSecretsRetention); an explicit
	// value (including "keep", used by dev/demo tenants for reproducible
	// debugging) always wins. Persist the normalized String() form so later
	// reads never have to re-parse a client-supplied spelling.
	retentionStr := req.SecretsRetention
	if retentionStr == "" {
		retentionStr = s.secretsRetention.String()
	}
	retentionPolicy, err := model.ParseSecretsRetention(retentionStr)
	if err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: err.Error(),
		})
		return
	}

	// SecretNameForInput derives a name by collapsing every character
	// outside [A-Z0-9] to '_', so distinct input ids (e.g. "pw-1" and
	// "pw_1") — or an input id and a user-supplied secrets key — can
	// collide on the same derived name. Detect that up front, before
	// mutating req.Secrets/req.Inputs below, and refuse the submission
	// rather than let one value silently overwrite another (#260 M12).
	derivedFrom := make(map[string]string, len(wf.SecretInputs)) // derived name -> input id that claimed it
	for _, id := range wf.SecretInputs {
		if _, ok := req.Inputs[id]; !ok {
			continue // optional input, not supplied: nothing to derive.
		}
		derived := model.SecretNameForInput(id)
		if other, taken := derivedFrom[derived]; taken {
			msg := fmt.Sprintf("declared secret inputs %q and %q both derive secret name %q",
				other, id, derived)
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError(msg, model.FieldError{Field: id, Message: msg}))
			return
		}
		derivedFrom[derived] = id
		if _, exists := req.Secrets[derived]; exists {
			msg := fmt.Sprintf("declared secret input %q derives secret name %q, which is also a key in the supplied secrets map",
				id, derived)
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError(msg, model.FieldError{Field: id, Message: msg}))
			return
		}
	}

	// cwltool:Secrets stripping: move the value of every workflow input
	// declared secret out of req.Inputs and into the submission's secrets
	// store, replacing it with the shared placeholder, BEFORE Inputs /
	// SubmittedInputs are captured below. A declared-but-absent input
	// (optional input, not supplied) is left alone. See
	// pkg/model.SecretNameForInput for the key the worker derives back.
	secrets := req.Secrets
	for _, id := range wf.SecretInputs {
		v, ok := req.Inputs[id]
		if !ok {
			continue
		}
		strVal, isStr := v.(string)
		if !isStr {
			respondError(w, reqID, http.StatusBadRequest,
				model.NewValidationError("declared secret input must be a string value",
					model.FieldError{Field: id, Message: "input is declared secret via cwltool:Secrets and must be a string"}))
			return
		}
		if secrets == nil {
			secrets = map[string]string{}
		}
		secrets[model.SecretNameForInput(id)] = strVal
		req.Inputs[id] = model.SecretInputPlaceholder
	}

	var secretNames []string
	for name := range secrets {
		secretNames = append(secretNames, name)
	}
	sort.Strings(secretNames)

	now := time.Now().UTC()
	sub := &model.Submission{
		ID:                "sub_" + uuid.New().String(),
		WorkflowID:        wf.ID,
		WorkflowName:      wf.Name,
		State:             model.SubmissionStatePending,
		Inputs:            req.Inputs,
		Outputs:           map[string]any{},
		Labels:            req.Labels,
		SubmittedBy:       userCtx.User.Username,
		UserToken:         userCtx.Token,
		TokenExpiry:       userCtx.Expiry,
		AuthProvider:      string(userCtx.Provider),
		OutputDestination: req.OutputDestination,
		CreatedAt:         now,
		Secrets:           secrets,
		SecretNames:       secretNames,
		SecretsRetention:  retentionPolicy.String(),
	}
	if sub.Inputs == nil {
		sub.Inputs = map[string]any{}
	}
	if sub.Labels == nil {
		sub.Labels = map[string]string{}
	}

	// Materialize file literals in submission inputs.
	// CWL file literals are File objects with "contents" but no path/location.
	// These must be written to temp files before execution.
	for k, v := range sub.Inputs {
		if materialized, err := fileliteral.MaterializeRecursive(v); err != nil {
			s.logger.Warn("materialize file literal", "input", k, "error", err)
		} else {
			sub.Inputs[k] = materialized
		}
	}

	if err := s.store.CreateSubmission(r.Context(), sub); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Create a StepInstance for each workflow step (3-level state architecture).
	// StepInstances track step lifecycle; Tasks are created later by the scheduler
	// when a step is dispatched.
	stepInstances := make([]*model.StepInstance, 0, len(wf.Steps))
	for _, step := range wf.Steps {
		stepInstances = append(stepInstances, &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		})
	}
	if err := s.store.BatchCreateStepInstances(r.Context(), stepInstances); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	s.logger.Info("submission created", "id", sub.ID, "workflow_id", wf.ID, "steps", len(wf.Steps))

	// Tasks is empty for a newly created submission (tasks are created later
	// by the scheduler), but set it to a non-nil slice for clean JSON output.
	sub.Tasks = []model.Task{}

	respondCreated(w, reqID, newSubmissionResponse(sub))
}

func (s *Server) handleListSubmissions(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	opts := parseListOptions(r)

	// Child submissions (scatter/sub-workflow fan-out) are hidden by default
	// so a 64-shard run does not flood the listing; pass include_children=true
	// to see them.
	opts.ExcludeChildren = r.URL.Query().Get("include_children") != "true"

	// Non-admin users only see their own submissions.
	userCtx := UserFromContext(r.Context())
	if userCtx != nil && !userCtx.User.IsAdmin() {
		opts.SubmittedBy = userCtx.User.Username
	}

	if opts.WorkflowID != "" {
		// workflow_id may be an exact workflow ID (single version — keep
		// current single-version semantics) or a workflow name (which is not
		// unique: re-registering under the same name creates a new ID/row).
		// Resolve by ID only; if that fails, filter by name so submissions
		// across every version sharing that name are returned. Do not use
		// resolveWorkflow/GetWorkflowByName here — that resolves a name to a
		// single newest ID and would silently drop older submissions.
		if wf, err := s.store.GetWorkflow(r.Context(), opts.WorkflowID); err == nil && wf != nil {
			opts.WorkflowID = wf.ID
		} else {
			opts.WorkflowName = opts.WorkflowID
			opts.WorkflowID = ""
		}
	}

	subs, total, err := s.store.ListSubmissions(r.Context(), opts)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Compute task summaries in a single batch query.
	if len(subs) > 0 {
		ids := make([]string, len(subs))
		for i, sub := range subs {
			ids[i] = sub.ID
		}
		if summaries, err := s.store.GetTaskSummaries(r.Context(), ids); err == nil {
			for _, sub := range subs {
				if ts, ok := summaries[sub.ID]; ok {
					sub.TaskSummary = ts
				}
			}
		}
	}

	respondList(w, reqID, newSubmissionListResponse(subs), &model.Pagination{
		Total:   total,
		Limit:   opts.Limit,
		Offset:  opts.Offset,
		HasMore: opts.Offset+opts.Limit < total,
	})
}

func (s *Server) handleGetSubmission(w http.ResponseWriter, r *http.Request) {
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

	// Compute task summary via aggregate query.
	if summaries, err := s.store.GetTaskSummaries(r.Context(), []string{sub.ID}); err == nil {
		if ts, ok := summaries[sub.ID]; ok {
			sub.TaskSummary = ts
		}
	}

	respondOK(w, reqID, newSubmissionResponse(sub))
}

func (s *Server) handleCancelSubmission(w http.ResponseWriter, r *http.Request) {
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

	// Ownership check: non-admin users can only cancel their own submissions.
	userCtx := UserFromContext(r.Context())
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	if !sub.State.CanTransitionTo(model.SubmissionStateCancelled) {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrValidation,
			Message: "cannot cancel submission in state " + string(sub.State),
		})
		return
	}

	now := time.Now().UTC()
	sub.State = model.SubmissionStateCancelled
	sub.CompletedAt = &now

	// The full cancel sequence (persist, cancel non-terminal steps/tasks,
	// synchronous descendant fan-out, metrics) is shared with the web UI's
	// cancel handler via internal/cancelseq — see that package's doc comment
	// for why it's the seam (closes #185: the UI handler used to skip all of
	// this). Fan-out is a belt over the scheduler's braces:
	// CancelNonTerminalTasks deliberately excludes subworkflow proxies so
	// the scheduler's per-tick reconciliation can cascade the cancel one
	// nesting level per tick — but that leaves children RUNNING until the
	// scheduler gets around to them. Cancelling them here, at cancel-accept
	// time, means workers see the kill signal on their next heartbeat even
	// if the scheduler is slow or stopped. Every write is CAS, so this is
	// idempotent against a concurrently-running scheduler cascade (and vice
	// versa).
	result, err := cancelseq.Run(r.Context(), s.store, s.metrics, s.logger, sub, now)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondOK(w, reqID, map[string]any{
		"id":                      sub.ID,
		"state":                   sub.State,
		"steps_cancelled":         result.StepsCancelled,
		"tasks_cancelled":         result.TasksCancelled,
		"tasks_already_completed": max(0, len(sub.Tasks)-result.TasksCancelled),
		"children_cancelled":      result.ChildrenCancelled,
	})
}

// cancelDescendantSubmissions is a thin wrapper around
// cancelseq.CancelDescendants, kept so existing tests that exercise the
// descendant fan-out directly (bypassing the HTTP handler) don't need to
// import internal/cancelseq themselves.
func (s *Server) cancelDescendantSubmissions(ctx context.Context, submissionID string, now time.Time, visited map[string]bool) int {
	return cancelseq.CancelDescendants(ctx, s.store, s.metrics, s.logger, submissionID, now, visited)
}

// hasActiveChildSubmissions reports whether any child submission spawned by the
// given submission's subworkflow proxy tasks (recursively, any depth) is still
// non-terminal. The visited set guards against cycles.
func (s *Server) hasActiveChildSubmissions(ctx context.Context, submissionID string, visited map[string]bool) (bool, error) {
	if visited[submissionID] {
		return false, nil
	}
	visited[submissionID] = true

	tasks, err := s.store.ListTasksBySubmission(ctx, submissionID)
	if err != nil {
		return false, fmt.Errorf("list tasks for %s: %w", submissionID, err)
	}
	for _, task := range tasks {
		if task.ExecutorType != model.ExecutorTypeSubworkflow {
			continue
		}
		children, err := s.store.GetChildSubmissions(ctx, task.ID)
		if err != nil {
			return false, fmt.Errorf("list children of task %s: %w", task.ID, err)
		}
		for _, child := range children {
			if !child.State.IsTerminal() {
				return true, nil
			}
			active, err := s.hasActiveChildSubmissions(ctx, child.ID, visited)
			if err != nil {
				return false, err
			}
			if active {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Server) handleDeleteSubmission(w http.ResponseWriter, r *http.Request) {
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

	// Deletion is a permanent, cascading operation (unlike the reversible cancel),
	// so require an authenticated, non-anonymous principal: in --allow-anonymous
	// mode all callers share the anonymous identity, and requireSubmissionAccess
	// treats a nil/anonymous context as authorized — acceptable for read/cancel
	// but not for an irreversible delete of another user's submission.
	userCtx := UserFromContext(r.Context())
	if userCtx == nil || userCtx.User == nil || userCtx.User.IsAnonymous() {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "deleting a submission requires authentication",
		})
		return
	}
	// Ownership check: non-admin users can only delete their own submissions.
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	// Deleting the rows would also delete any subworkflow proxy tasks — the
	// scheduler's only handle for cascading a cancel to child submissions. If
	// active children exist (at any nesting depth), refuse: cancel first and
	// let the cascade finish, then delete.
	if active, err := s.hasActiveChildSubmissions(r.Context(), id, map[string]bool{}); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	} else if active {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "submission has active child submissions; cancel it and wait for them to finish before deleting",
		})
		return
	}

	// Cancel any in-flight work before deleting rows so workers see the
	// cancellation on their next poll rather than reporting to a missing task.
	now := time.Now().UTC()
	stepsCancelled, _ := s.store.CancelNonTerminalSteps(r.Context(), id, now)
	tasksCancelled, _ := s.store.CancelNonTerminalTasks(r.Context(), id, now)

	if err := s.store.DeleteSubmission(r.Context(), id); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	respondOK(w, reqID, map[string]any{
		"id":              id,
		"deleted":         true,
		"steps_cancelled": stepsCancelled,
		"tasks_cancelled": tasksCancelled,
	})
}

func (s *Server) handleRetrySubmission(w http.ResponseWriter, r *http.Request) {
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

	// Ownership check: non-admin users can only retry their own submissions.
	userCtx := UserFromContext(r.Context())
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	if sub.State != model.SubmissionStateFailed {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrValidation,
			Message: "cannot retry submission in state " + string(sub.State) + "; only FAILED submissions can be retried",
		})
		return
	}

	// Secrets purged (retention sweep or manual DELETE .../secrets) means the
	// scheduler has nothing to re-attach to the retried tasks: refuse rather
	// than silently run without them. Resubmitting (with secrets again) is
	// the supported path.
	if sub.SecretsState() == "purged" {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "secrets purged; resubmit with secrets",
		})
		return
	}

	// Reset the submission to RUNNING so the scheduler picks it up.
	sub.State = model.SubmissionStateRunning
	sub.Error = nil
	sub.CompletedAt = nil
	sub.OutputState = ""

	if err := s.store.UpdateSubmission(r.Context(), sub); err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	// Batch-reset FAILED steps and tasks.
	stepsReset, err := s.store.ResetFailedSteps(r.Context(), sub.ID)
	if err != nil {
		s.logger.Error("retry: reset failed steps", "submission_id", sub.ID, "error", err)
	}
	tasksReset, err := s.store.ResetFailedTasks(r.Context(), sub.ID)
	if err != nil {
		s.logger.Error("retry: reset failed tasks", "submission_id", sub.ID, "error", err)
	}

	s.logger.Info("submission retried",
		"id", sub.ID,
		"steps_reset", stepsReset,
		"tasks_reset", tasksReset,
	)

	respondOK(w, reqID, map[string]any{
		"id":          sub.ID,
		"state":       sub.State,
		"steps_reset": stepsReset,
		"tasks_reset": tasksReset,
	})
}

// handleDeleteSubmissionSecrets implements DELETE /api/v1/submissions/{id}/secrets:
// an owner/admin-only manual purge of a submission's secret values (the
// "manual" row of the retention table — see pkg/model.SecretsRetentionPolicy
// and the scheduler's retention sweep for the automatic policies). Purges
// this submission and, recursively, every descendant child submission
// reached through its sub-workflow proxy tasks, so a re-delivered secret
// cannot survive in a nested child after the parent's is gone.
func (s *Server) handleDeleteSubmissionSecrets(w http.ResponseWriter, r *http.Request) {
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

	// Ownership check: non-admin users can only purge their own submissions.
	userCtx := UserFromContext(r.Context())
	if !requireSubmissionAccess(sub, userCtx) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code: model.ErrForbidden, Message: "access denied: you can only access your own submissions",
		})
		return
	}

	if sub.SecretsState() != "present" {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    model.ErrNotFound,
			Message: "submission has no secrets to purge",
		})
		return
	}

	// A purge while the submission is still non-terminal would delete the
	// secrets a running task depends on out from under it (and, before H2,
	// the scheduler's terminal scrub would never even fire to leave a
	// task-row copy behind — this guard closes that window entirely). The
	// retention sweep never sees a non-terminal submission in the first
	// place (ListSubmissionsWithSecretsForRetention only returns terminal
	// rows), so this only affects the manual DELETE path.
	if !sub.State.IsTerminal() {
		respondError(w, reqID, http.StatusConflict, &model.APIError{
			Code:    model.ErrConflict,
			Message: "submission is not terminal; cancel it first or wait for completion",
		})
		return
	}

	now := time.Now().UTC()
	purged, err := s.purgeSecretsCascade(r.Context(), sub.ID, now, map[string]bool{})
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			&model.APIError{Code: model.ErrInternal, Message: err.Error()})
		return
	}

	sub.Secrets = nil
	sub.SecretsPurgedAt = &now

	s.logger.Info("submission secrets purged", "id", sub.ID, "names", len(sub.SecretNames), "submissions_purged", purged)

	respondOK(w, reqID, map[string]any{
		"id":                 sub.ID,
		"secrets_state":      sub.SecretsState(),
		"secret_names":       sub.SecretNames,
		"secrets_retention":  sub.SecretsRetention,
		"secrets_purged_at":  sub.SecretsPurgedAt,
		"submissions_purged": purged,
	})
}

// purgeSecretsCascade purges secret values for the submission id and every
// descendant child submission reachable through its subworkflow proxy tasks
// (any nesting depth), following the same task -> GetChildSubmissions link
// hasActiveChildSubmissions/cancelseq use. store.PurgeSubmissionSecrets is a
// no-op on a submission that already has no secret value (already NULL), so
// purging every descendant unconditionally is safe and sidesteps
// GetChildSubmissions' lean column set (it does not load secret metadata,
// so child.SecretsState() would be unreliable here). visited guards against
// cycles; returns the number of submissions purged (including id itself).
func (s *Server) purgeSecretsCascade(ctx context.Context, id string, now time.Time, visited map[string]bool) (int, error) {
	if visited[id] {
		return 0, nil
	}
	visited[id] = true

	if err := s.store.PurgeSubmissionSecrets(ctx, id, now); err != nil {
		return 0, fmt.Errorf("purge submission %s: %w", id, err)
	}
	// PurgeSubmissionSecrets only clears the submissions row; the per-task
	// subset of secrets delivered to this submission's tasks lives in
	// tasks.runtime_hints and survives it untouched otherwise (#260 H3).
	if _, err := s.store.ScrubTaskSecretsForSubmission(ctx, id); err != nil {
		return 0, fmt.Errorf("scrub task secrets for submission %s: %w", id, err)
	}
	count := 1

	tasks, err := s.store.ListTasksBySubmission(ctx, id)
	if err != nil {
		return count, fmt.Errorf("list tasks for %s: %w", id, err)
	}
	for _, task := range tasks {
		if task.ExecutorType != model.ExecutorTypeSubworkflow {
			continue
		}
		children, err := s.store.GetChildSubmissions(ctx, task.ID)
		if err != nil {
			return count, fmt.Errorf("list children of task %s: %w", task.ID, err)
		}
		for _, child := range children {
			n, err := s.purgeSecretsCascade(ctx, child.ID, now, visited)
			count += n
			if err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

// buildDryRunReport validates a workflow and inputs without creating a submission.
func (s *Server) buildDryRunReport(wf *model.Workflow, inputs map[string]any) map[string]any {
	var errors []map[string]string
	var warnings []map[string]string

	if inputs == nil {
		inputs = map[string]any{}
	}

	// --- Input validation ---
	inputsValid := true
	provided := make(map[string]bool, len(inputs))
	for k := range inputs {
		provided[k] = true
	}

	// Check for missing required inputs.
	for _, inp := range wf.Inputs {
		if inp.Required && inp.Default == nil {
			if !provided[inp.ID] {
				inputsValid = false
				errors = append(errors, map[string]string{
					"field":   "inputs." + inp.ID,
					"message": "required input " + inp.ID + " is missing",
				})
			}
		}
		delete(provided, inp.ID)
	}

	// Check for unknown inputs.
	for k := range provided {
		warnings = append(warnings, map[string]string{
			"field":   "inputs." + k,
			"message": "unknown input " + k + " (not declared in workflow)",
		})
	}

	// Warn about Directory inputs that look like they may not exist.
	for _, inp := range wf.Inputs {
		baseType := strings.TrimSuffix(inp.Type, "?")
		if baseType != "Directory" {
			continue
		}
		val, _ := inputs[inp.ID].(string)
		if val == "" {
			continue
		}
		// Bare string without scheme — likely a workspace path; warn if it
		// doesn't start with / (common typo).
		if !strings.HasPrefix(val, "/") && !strings.Contains(val, "://") {
			warnings = append(warnings, map[string]string{
				"field":   "inputs." + inp.ID,
				"message": "Directory path " + val + " does not start with / or contain a scheme",
			})
		}
	}

	// --- Step analysis ---
	steps := make([]map[string]any, 0, len(wf.Steps))
	executorSet := make(map[model.ExecutorType]bool)

	for _, step := range wf.Steps {
		execType := model.ExecutorTypeLocal
		// Executor selection: force > hint > auto-promote > default > local.
		if s.config.ForceExecutor != "" {
			execType = model.ExecutorType(s.config.ForceExecutor)
		} else if step.Hints != nil && step.Hints.ExecutorType != "" && step.Hints.ExecutorType != model.ExecutorTypeContainer {
			execType = step.Hints.ExecutorType
		} else if step.Hints != nil && step.Hints.DockerImage != "" {
			execType = model.ExecutorTypeWorker
		} else if s.config.DefaultExecutor != "" {
			execType = model.ExecutorType(s.config.DefaultExecutor)
		}
		executorSet[execType] = true

		available := s.registry != nil && s.registry.Has(execType)
		if !available {
			errors = append(errors, map[string]string{
				"field":   "steps." + step.ID,
				"message": "executor " + string(execType) + " is not available",
			})
		}

		stepInfo := map[string]any{
			"id":                 step.ID,
			"executor_type":      string(execType),
			"depends_on":         step.DependsOn,
			"executor_available": available,
		}
		if step.Hints != nil && step.Hints.BVBRCAppID != "" {
			stepInfo["bvbrc_app_id"] = step.Hints.BVBRCAppID
		}
		steps = append(steps, stepInfo)
	}

	// --- Execution order (topological sort from DependsOn) ---
	order := topoSort(wf.Steps)
	dagAcyclic := len(order) == len(wf.Steps)
	if !dagAcyclic {
		errors = append(errors, map[string]string{
			"field":   "steps",
			"message": "cyclic dependency detected",
		})
	}

	// --- Executor availability summary ---
	execAvail := make(map[string]string, len(executorSet))
	for et := range executorSet {
		if s.registry != nil && s.registry.Has(et) {
			execAvail[string(et)] = "available"
		} else {
			execAvail[string(et)] = "unavailable"
		}
	}

	valid := len(errors) == 0

	// Ensure non-nil slices for clean JSON.
	if errors == nil {
		errors = []map[string]string{}
	}
	if warnings == nil {
		warnings = []map[string]string{}
	}

	return map[string]any{
		"dry_run":               true,
		"valid":                 valid,
		"workflow":              map[string]any{"id": wf.ID, "name": wf.Name, "step_count": len(wf.Steps)},
		"inputs_valid":          inputsValid,
		"steps":                 steps,
		"dag_acyclic":           dagAcyclic,
		"execution_order":       order,
		"executor_availability": execAvail,
		"errors":                errors,
		"warnings":              warnings,
	}
}

// topoSort returns step IDs in topological order using Kahn's algorithm.
// Returns a partial result if a cycle exists.
func topoSort(steps []model.Step) []string {
	inDegree := make(map[string]int, len(steps))
	forward := make(map[string][]string, len(steps))

	for _, s := range steps {
		if _, ok := inDegree[s.ID]; !ok {
			inDegree[s.ID] = 0
		}
		for _, dep := range s.DependsOn {
			forward[dep] = append(forward[dep], s.ID)
			inDegree[s.ID]++
		}
	}

	var queue, order []string
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		for _, succ := range forward[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	return order
}

// validateAnonymousSubmission checks if an anonymous user can submit this workflow.
// Returns an error if the workflow uses executors not allowed for anonymous users.
func (s *Server) validateAnonymousSubmission(wf *model.Workflow) error {
	if s.anonConfig == nil {
		return nil
	}

	for _, step := range wf.Steps {
		execType := model.ExecutorTypeLocal
		// Executor selection: force > hint > auto-promote > default > local.
		if s.config.ForceExecutor != "" {
			execType = model.ExecutorType(s.config.ForceExecutor)
		} else if step.Hints != nil && step.Hints.ExecutorType != "" && step.Hints.ExecutorType != model.ExecutorTypeContainer {
			execType = step.Hints.ExecutorType
		} else if step.Hints != nil && step.Hints.DockerImage != "" {
			execType = model.ExecutorTypeWorker
		} else if s.config.DefaultExecutor != "" {
			execType = model.ExecutorType(s.config.DefaultExecutor)
		}

		if !s.anonConfig.IsExecutorAllowed(execType) {
			return fmt.Errorf("anonymous users cannot use executor type %q; authentication required", execType)
		}
	}

	return nil
}

// resolveWorkflow looks up a workflow by ID first, then falls back to name lookup.
// This allows workflow_id to accept either "wf_..." IDs or workflow names.
func (s *Server) resolveWorkflow(ctx context.Context, idOrName string) (*model.Workflow, error) {
	// Try by ID first.
	wf, err := s.store.GetWorkflow(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	if wf != nil {
		return wf, nil
	}

	// Fall back to name lookup.
	return s.store.GetWorkflowByName(ctx, idOrName)
}
