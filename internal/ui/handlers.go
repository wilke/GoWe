package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// UI handles the web user interface.
type UI struct {
	store           store.Store
	sessions        *SessionManager
	logger          *slog.Logger
	bvbrcCaller     bvbrc.RPCCaller // AppService caller
	workspaceCaller bvbrc.RPCCaller // Workspace service caller
	startTime       time.Time
	secure          bool // Use secure cookies (HTTPS)
}

// Config holds UI configuration.
type Config struct {
	Secure bool // Use secure cookies for HTTPS
}

// New creates a new UI handler.
func New(st store.Store, logger *slog.Logger, cfg Config) *UI {
	return &UI{
		store:     st,
		sessions:  NewSessionManager(st),
		logger:    logger.With("component", "ui"),
		startTime: time.Now(),
		secure:    cfg.Secure,
	}
}

// WithBVBRCCaller sets the BV-BRC RPC caller for AppService operations.
func (ui *UI) WithBVBRCCaller(caller bvbrc.RPCCaller) {
	ui.bvbrcCaller = caller
}

// WithWorkspaceCaller sets the BV-BRC RPC caller for Workspace operations.
func (ui *UI) WithWorkspaceCaller(caller bvbrc.RPCCaller) {
	ui.workspaceCaller = caller
}

// HandleLogin renders the login page.
func (ui *UI) HandleLogin(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard.
	if sess, _ := ui.sessions.GetSessionFromRequest(r); sess != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	data := map[string]any{
		"Title": "Login - GoWe",
		"Error": r.URL.Query().Get("error"),
	}
	ui.render(w, "login", data)
}

// HandleLoginPost processes the login form.
func (ui *UI) HandleLoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=Invalid+request", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	if username == "" || password == "" {
		http.Redirect(w, r, "/login?error=Username+and+password+required", http.StatusSeeOther)
		return
	}

	// Authenticate with BV-BRC.
	token, err := ui.authenticateBVBRC(r.Context(), username, password)
	if err != nil {
		ui.logger.Warn("login failed", "username", username, "error", err)
		http.Redirect(w, r, "/login?error=Invalid+credentials", http.StatusSeeOther)
		return
	}

	// Parse token to get expiry and canonical username.
	tokenInfo := bvbrc.ParseToken(token)

	// Use token username (e.g., "awilke@bvbrc") for session - this is needed for workspace paths.
	sessionUsername := tokenInfo.Username
	if sessionUsername == "" {
		sessionUsername = username // Fallback to form input if token parsing fails
	}

	// Determine role (admin list can be configured via env or config).
	role := model.RoleUser
	if ui.isAdminUser(sessionUsername) {
		role = model.RoleAdmin
	}

	// Create session.
	sess, err := ui.sessions.CreateSession(r.Context(), sessionUsername, sessionUsername, role, token, tokenInfo.Expiry)
	if err != nil {
		ui.logger.Error("create session failed", "error", err)
		http.Redirect(w, r, "/login?error=Session+creation+failed", http.StatusSeeOther)
		return
	}

	// Set session cookie.
	SetSessionCookie(w, sess, ui.secure)

	ui.logger.Info("user logged in", "username", username, "session", sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleLogout clears the session and redirects to login.
func (ui *UI) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, _ := ui.sessions.GetSessionFromRequest(r); sess != nil {
		_ = ui.sessions.DeleteSession(r.Context(), sess.ID)
		ui.logger.Info("user logged out", "username", sess.Username, "session", sess.ID)
	}
	ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// HandleDashboard renders the main dashboard.
func (ui *UI) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	// Get workflow count.
	workflows, workflowCount, _ := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 5})

	// Get recent submissions.
	submissions, submissionCount, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{Limit: 5})

	// Compute submission stats by state.
	pendingCount := 0
	runningCount := 0
	completedCount := 0
	failedCount := 0
	for _, sub := range submissions {
		switch sub.State {
		case model.SubmissionStatePending:
			pendingCount++
		case model.SubmissionStateRunning:
			runningCount++
		case model.SubmissionStateCompleted:
			completedCount++
		case model.SubmissionStateFailed:
			failedCount++
		}
	}

	data := map[string]any{
		"Title":           "Dashboard - GoWe",
		"Session":         sess,
		"WorkflowCount":   workflowCount,
		"SubmissionCount": submissionCount,
		"RecentWorkflows": workflows,
		"RecentSubmissions": submissions,
		"Stats": map[string]int{
			"Pending":   pendingCount,
			"Running":   runningCount,
			"Completed": completedCount,
			"Failed":    failedCount,
		},
		"Uptime": time.Since(ui.startTime).Round(time.Second).String(),
	}
	ui.render(w, "dashboard", data)
}

// --- Workflow Handlers ---

// HandleWorkflowList renders the workflow list page.
func (ui *UI) HandleWorkflowList(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	opts := ui.parseListOptions(r)

	workflows, total, err := ui.store.ListWorkflows(r.Context(), opts)
	if err != nil {
		ui.renderError(w, "Failed to load workflows", err)
		return
	}

	data := map[string]any{
		"Title":      "Workflows - GoWe",
		"Session":    sess,
		"Workflows":  workflows,
		"Pagination": ui.buildPagination(opts, total),
	}
	ui.render(w, "workflows/list", data)
}

// HandleWorkflowDetail renders a single workflow.
func (ui *UI) HandleWorkflowDetail(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	wf, err := ui.store.GetWorkflow(r.Context(), id)
	if err != nil {
		ui.renderError(w, "Failed to load workflow", err)
		return
	}
	if wf == nil {
		ui.renderNotFound(w, "Workflow not found")
		return
	}

	data := map[string]any{
		"Title":    wf.Name + " - GoWe",
		"Session":  sess,
		"Workflow": wf,
	}
	ui.render(w, "workflows/detail", data)
}

// HandleWorkflowCreate renders the workflow creation form.
func (ui *UI) HandleWorkflowCreate(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	data := map[string]any{
		"Title":   "Create Workflow - GoWe",
		"Session": sess,
		"Error":   r.URL.Query().Get("error"),
	}
	ui.render(w, "workflows/create", data)
}

// HandleWorkflowDelete deletes a workflow (HTMX).
func (ui *UI) HandleWorkflowDelete(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	if err := ui.store.DeleteWorkflow(r.Context(), id); err != nil {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Return empty response for HTMX to remove the element.
	w.WriteHeader(http.StatusOK)
}

// --- Submission Handlers ---

// HandleSubmissionList renders the submission list page.
func (ui *UI) HandleSubmissionList(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	opts := ui.parseListOptions(r)

	// Parse date filters
	dateStart := r.URL.Query().Get("date_start")
	dateEnd := r.URL.Query().Get("date_end")
	if dateStart != "" {
		opts.DateStart = dateStart
	}
	if dateEnd != "" {
		opts.DateEnd = dateEnd
	}

	submissions, total, err := ui.store.ListSubmissions(r.Context(), opts)
	if err != nil {
		ui.renderError(w, "Failed to load submissions", err)
		return
	}

	// Calculate queue position for pending submissions
	queuePosition := 1

	// Get task summaries and tasks for each submission.
	for _, sub := range submissions {
		tasks, _ := ui.store.ListTasksBySubmission(r.Context(), sub.ID)
		taskList := make([]model.Task, len(tasks))
		for i, t := range tasks {
			taskList[i] = *t
		}
		sub.Tasks = taskList
		sub.TaskSummary = model.ComputeTaskSummary(taskList)

		// Set queue position for pending submissions
		if sub.State == model.SubmissionStatePending {
			sub.QueuePosition = queuePosition
			queuePosition++
		}
	}

	data := map[string]any{
		"Title":       "Submissions - GoWe",
		"Session":     sess,
		"Submissions": submissions,
		"Pagination":  ui.buildPagination(opts, total),
		"StateFilter": opts.State,
		"DateStart":   dateStart,
		"DateEnd":     dateEnd,
	}
	ui.render(w, "submissions/list", data)
}

// HandleSubmissionDetail renders a single submission with its tasks.
func (ui *UI) HandleSubmissionDetail(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil {
		ui.renderError(w, "Failed to load submission", err)
		return
	}
	if sub == nil {
		ui.renderNotFound(w, "Submission not found")
		return
	}

	// Compute task summary
	sub.TaskSummary = model.ComputeTaskSummary(sub.Tasks)

	// Calculate queue position if pending
	if sub.State == model.SubmissionStatePending {
		pendingSubs, _, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{
			State: "PENDING",
			Limit: 1000,
		})
		for i, ps := range pendingSubs {
			if ps.ID == sub.ID {
				sub.QueuePosition = i + 1
				break
			}
		}
	}

	// Load workflow for DAG visualization
	var workflow *model.Workflow
	if sub.WorkflowID != "" {
		workflow, _ = ui.store.GetWorkflow(r.Context(), sub.WorkflowID)
	}

	data := map[string]any{
		"Title":      fmt.Sprintf("Submission %s - GoWe", sub.ID),
		"Session":    sess,
		"Submission": sub,
		"Workflow":   workflow,
	}
	ui.render(w, "submissions/detail", data)
}

// HandleSubmissionCreate renders the submission creation form.
func (ui *UI) HandleSubmissionCreate(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	// Load available workflows.
	workflows, _, _ := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 100})

	// Pre-select workflow if ID provided.
	workflowID := r.URL.Query().Get("workflow_id")
	var selectedWorkflow *model.Workflow
	if workflowID != "" {
		selectedWorkflow, _ = ui.store.GetWorkflow(r.Context(), workflowID)
	}

	// Build workspace path for file picker.
	// Workspace is available if user has a session with a token (even if no global workspace caller).
	workspacePath := ""
	hasWorkspace := false
	if sess != nil && sess.Username != "" && sess.Token != "" {
		workspacePath = "/" + sess.Username + "/home"
		hasWorkspace = true
	}

	data := map[string]any{
		"Title":            "Submit Workflow - GoWe",
		"Session":          sess,
		"Workflows":        workflows,
		"SelectedWorkflow": selectedWorkflow,
		"WorkspacePath":    workspacePath,
		"HasWorkspace":     hasWorkspace,
		"Error":            r.URL.Query().Get("error"),
	}
	ui.render(w, "submissions/create", data)
}

// HandleSubmissionCreatePost processes the submission creation form.
func (ui *UI) HandleSubmissionCreatePost(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/submissions/new?error=Invalid+request", http.StatusSeeOther)
		return
	}

	workflowID := r.FormValue("workflow_id")
	if workflowID == "" {
		http.Redirect(w, r, "/submissions/new?error=Workflow+is+required", http.StatusSeeOther)
		return
	}

	wf, err := ui.store.GetWorkflow(r.Context(), workflowID)
	if err != nil || wf == nil {
		http.Redirect(w, r, "/submissions/new?error=Workflow+not+found", http.StatusSeeOther)
		return
	}

	// Collect inputs from form fields named inputs[key].
	inputs := make(map[string]any)
	for _, inp := range wf.Inputs {
		val := r.FormValue("inputs[" + inp.ID + "]")
		if val == "" {
			if inp.Default != nil {
				inputs[inp.ID] = inp.Default
			}
			continue
		}
		// Coerce values based on declared type.
		switch {
		case inp.Type == "int" || inp.Type == "int?":
			if n, err := strconv.Atoi(val); err == nil {
				inputs[inp.ID] = n
				continue
			}
		case inp.Type == "float" || inp.Type == "double" || inp.Type == "float?" || inp.Type == "double?":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				inputs[inp.ID] = f
				continue
			}
		case inp.Type == "boolean" || inp.Type == "boolean?":
			inputs[inp.ID] = val == "true" || val == "on" || val == "1"
			continue
		}
		inputs[inp.ID] = val
	}

	// Parse optional labels JSON.
	labels := map[string]string{}
	if labelsStr := r.FormValue("labels"); labelsStr != "" {
		_ = json.Unmarshal([]byte(labelsStr), &labels)
	}

	now := time.Now().UTC()
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       labels,
		CreatedAt:    now,
	}
	if sess != nil {
		sub.SubmittedBy = sess.Username
	}

	if err := ui.store.CreateSubmission(r.Context(), sub); err != nil {
		ui.logger.Error("create submission failed", "error", err)
		http.Redirect(w, r, "/submissions/new?workflow_id="+workflowID+"&error=Failed+to+create+submission", http.StatusSeeOther)
		return
	}

	// Create tasks for each workflow step.
	for _, step := range wf.Steps {
		var execType model.ExecutorType
		var bvbrcAppID string
		if step.Hints != nil {
			if step.Hints.ExecutorType != "" {
				execType = step.Hints.ExecutorType
			}
			bvbrcAppID = step.Hints.BVBRCAppID
		}

		task := &model.Task{
			ID:           "task_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.TaskStatePending,
			ExecutorType: execType,
			BVBRCAppID:   bvbrcAppID,
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			DependsOn:    step.DependsOn,
			MaxRetries:   3,
			CreatedAt:    now,
		}
		if err := ui.store.CreateTask(r.Context(), task); err != nil {
			ui.logger.Error("create task failed", "step", step.ID, "error", err)
		}
	}

	ui.logger.Info("submission created via UI", "id", sub.ID, "workflow", wf.Name, "user", sub.SubmittedBy)
	http.Redirect(w, r, "/submissions/"+sub.ID, http.StatusSeeOther)
}

// HandleSubmissionCancel cancels a running submission (HTMX).
func (ui *UI) HandleSubmissionCancel(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if sub.State.IsTerminal() {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sub.State = model.SubmissionStateCancelled
	now := time.Now()
	sub.CompletedAt = &now

	if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Redirect to refresh the page.
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
}

// HandleTaskLogs renders task logs (stdout/stderr).
func (ui *UI) HandleTaskLogs(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	subID := ui.pathParam(r, "id")
	taskID := ui.pathParam(r, "tid")

	task, err := ui.store.GetTask(r.Context(), taskID)
	if err != nil || task == nil || task.SubmissionID != subID {
		ui.renderNotFound(w, "Task not found")
		return
	}

	data := map[string]any{
		"Title":        fmt.Sprintf("Task Logs %s - GoWe", task.ID),
		"Session":      sess,
		"Task":         task,
		"SubmissionID": subID,
	}
	ui.render(w, "submissions/task_logs", data)
}

// HandleSubmissionExport exports submissions as CSV.
func (ui *UI) HandleSubmissionExport(w http.ResponseWriter, r *http.Request) {
	opts := ui.parseListOptions(r)
	opts.Limit = 10000 // Export up to 10k records

	// Parse date filters
	if dateStart := r.URL.Query().Get("date_start"); dateStart != "" {
		opts.DateStart = dateStart
	}
	if dateEnd := r.URL.Query().Get("date_end"); dateEnd != "" {
		opts.DateEnd = dateEnd
	}

	submissions, _, err := ui.store.ListSubmissions(r.Context(), opts)
	if err != nil {
		http.Error(w, "Failed to load submissions", http.StatusInternalServerError)
		return
	}

	// Set CSV headers
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=submissions_%s.csv", time.Now().Format("20060102_150405")))

	// Write CSV header
	fmt.Fprintln(w, "ID,Workflow ID,Workflow Name,State,Submitted By,Created At,Completed At,Total Tasks,Completed Tasks,Failed Tasks")

	// Write data rows
	for _, sub := range submissions {
		tasks, _ := ui.store.ListTasksBySubmission(r.Context(), sub.ID)
		taskList := make([]model.Task, len(tasks))
		for i, t := range tasks {
			taskList[i] = *t
		}
		summary := model.ComputeTaskSummary(taskList)

		completedAt := ""
		if sub.CompletedAt != nil {
			completedAt = sub.CompletedAt.Format(time.RFC3339)
		}

		fmt.Fprintf(w, "%s,%s,%q,%s,%s,%s,%s,%d,%d,%d\n",
			sub.ID,
			sub.WorkflowID,
			sub.WorkflowName,
			sub.State,
			sub.SubmittedBy,
			sub.CreatedAt.Format(time.RFC3339),
			completedAt,
			summary.Total,
			summary.Success,
			summary.Failed,
		)
	}
}

// HandleSubmissionResume resumes a failed submission.
func (ui *UI) HandleSubmissionResume(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if sub.State != model.SubmissionStateFailed {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Reset failed tasks to PENDING
	for _, task := range sub.Tasks {
		if task.State == model.TaskStateFailed {
			task.State = model.TaskStatePending
			task.RetryCount = 0
			task.Stdout = ""
			task.Stderr = ""
			task.ExitCode = nil
			task.StartedAt = nil
			task.CompletedAt = nil
			if err := ui.store.UpdateTask(r.Context(), &task); err != nil {
				ui.logger.Error("failed to reset task", "task_id", task.ID, "error", err)
			}
		}
	}

	// Set submission back to RUNNING
	sub.State = model.SubmissionStateRunning
	sub.CompletedAt = nil
	if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	ui.logger.Info("submission resumed", "id", id)
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
}

// HandleRecomputeFailed recomputes all failed tasks in a submission.
func (ui *UI) HandleRecomputeFailed(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Reset all failed tasks
	recomputeCount := 0
	for _, task := range sub.Tasks {
		if task.State == model.TaskStateFailed {
			task.State = model.TaskStatePending
			task.RetryCount = 0
			task.Stdout = ""
			task.Stderr = ""
			task.ExitCode = nil
			task.StartedAt = nil
			task.CompletedAt = nil
			if err := ui.store.UpdateTask(r.Context(), &task); err != nil {
				ui.logger.Error("failed to reset task", "task_id", task.ID, "error", err)
			} else {
				recomputeCount++
			}
		}
	}

	// If submission was terminal, set it back to RUNNING
	if sub.State.IsTerminal() && recomputeCount > 0 {
		sub.State = model.SubmissionStateRunning
		sub.CompletedAt = nil
		if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
			ui.logger.Error("failed to update submission", "id", id, "error", err)
		}
	}

	ui.logger.Info("recomputed failed tasks", "id", id, "count", recomputeCount)
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
}

// HandleTaskRecompute recomputes a single task.
func (ui *UI) HandleTaskRecompute(w http.ResponseWriter, r *http.Request) {
	subID := ui.pathParam(r, "id")
	taskID := ui.pathParam(r, "tid")

	task, err := ui.store.GetTask(r.Context(), taskID)
	if err != nil || task == nil || task.SubmissionID != subID {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if task.State != model.TaskStateFailed {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Reset the task
	task.State = model.TaskStatePending
	task.RetryCount = 0
	task.Stdout = ""
	task.Stderr = ""
	task.ExitCode = nil
	task.StartedAt = nil
	task.CompletedAt = nil
	if err := ui.store.UpdateTask(r.Context(), task); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Set submission back to RUNNING if it was terminal
	sub, _ := ui.store.GetSubmission(r.Context(), subID)
	if sub != nil && sub.State.IsTerminal() {
		sub.State = model.SubmissionStateRunning
		sub.CompletedAt = nil
		ui.store.UpdateSubmission(r.Context(), sub)
	}

	ui.logger.Info("task recomputed", "task_id", taskID, "submission_id", subID)
	w.Header().Set("HX-Redirect", "/submissions/"+subID)
	w.WriteHeader(http.StatusOK)
}

// --- Admin Handlers ---

// HandleAdminStats renders system statistics.
func (ui *UI) HandleAdminStats(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	// Get counts.
	_, workflowCount, _ := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 1})
	_, submissionCount, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{Limit: 1})

	// Get submissions by state.
	pending, _, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{State: "PENDING", Limit: 1000})
	running, _, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{State: "RUNNING", Limit: 1000})
	completed, _, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{State: "COMPLETED", Limit: 1000})
	failed, _, _ := ui.store.ListSubmissions(r.Context(), model.ListOptions{State: "FAILED", Limit: 1000})

	data := map[string]any{
		"Title":           "System Stats - GoWe",
		"Session":         sess,
		"WorkflowCount":   workflowCount,
		"SubmissionCount": submissionCount,
		"SubmissionStats": map[string]int{
			"Pending":   len(pending),
			"Running":   len(running),
			"Completed": len(completed),
			"Failed":    len(failed),
		},
		"Uptime": time.Since(ui.startTime).Round(time.Second).String(),
	}
	ui.render(w, "admin/stats", data)
}

// HandleAdminHealth renders the health dashboard.
func (ui *UI) HandleAdminHealth(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	data := map[string]any{
		"Title":     "System Health - GoWe",
		"Session":   sess,
		"Uptime":    time.Since(ui.startTime).Round(time.Second).String(),
		"StartTime": ui.startTime.Format(time.RFC3339),
		"HasBVBRC":  ui.bvbrcCaller != nil,
	}
	ui.render(w, "admin/health", data)
}

// --- Workspace Handlers ---

// getWorkspaceCaller returns a workspace caller, using the session token if no global caller is configured.
func (ui *UI) getWorkspaceCaller(sess *model.Session) bvbrc.RPCCaller {
	if ui.workspaceCaller != nil {
		return ui.workspaceCaller
	}
	// Create a caller using the session token.
	if sess != nil && sess.Token != "" {
		cfg := bvbrc.ClientConfig{
			AppServiceURL: "https://p3.theseed.org/services/Workspace",
			Token:         sess.Token,
		}
		return bvbrc.NewHTTPRPCCaller(cfg, ui.logger)
	}
	return nil
}

// HandleWorkspaceAPI returns workspace listing as JSON (for file picker modal).
func (ui *UI) HandleWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	caller := ui.getWorkspaceCaller(sess)
	if caller == nil {
		http.Error(w, `{"error": "Workspace not configured - please log in"}`, http.StatusServiceUnavailable)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/" + sess.Username + "/home"
	}

	result, err := caller.Call(r.Context(), "Workspace.ls", []any{
		map[string]any{"paths": []string{path}},
	})
	if err != nil {
		ui.logger.Error("workspace API ls failed", "path", path, "error", err)
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Parse workspace response.
	var outer []map[string]json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		http.Error(w, `{"error": "Failed to parse workspace response"}`, http.StatusInternalServerError)
		return
	}

	var items [][]any
	// Select the listing corresponding to the requested path.
	var listing json.RawMessage
	if v, ok := outer[0][path]; ok {
		listing = v
	} else {
		trimmed := strings.TrimSuffix(path, "/")
		if trimmed != path {
			if v, ok := outer[0][trimmed]; ok {
				listing = v
			}
		}
	}

	if listing == nil {
		http.Error(w, fmt.Sprintf(`{"error": "Workspace listing for path %q not found in response"}`, path), http.StatusInternalServerError)
		return
	}

	if err := json.Unmarshal(listing, &items); err != nil {
		http.Error(w, `{"error": "Failed to parse workspace listing"}`, http.StatusInternalServerError)
		return
	}
	// Convert to structured response.
	type wsItem struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Size     int64  `json:"size"`
		IsFolder bool   `json:"isFolder"`
	}

	response := struct {
		Path  string   `json:"path"`
		Items []wsItem `json:"items"`
	}{
		Path:  path,
		Items: make([]wsItem, 0, len(items)),
	}

	for _, item := range items {
		if len(item) < 2 {
			continue
		}
		itemPath, _ := item[0].(string)
		itemType, _ := item[1].(string)
		var itemSize int64
		if len(item) > 6 {
			if size, ok := item[6].(float64); ok {
				itemSize = int64(size)
			}
		}

		// Extract name from path.
		// The workspace API may return full paths or just names.
		// Ensure we have a full path.
		name := itemPath
		fullPath := itemPath
		if strings.Contains(itemPath, "/") {
			// Already a full path - extract name from it
			parts := strings.Split(strings.TrimSuffix(itemPath, "/"), "/")
			name = parts[len(parts)-1]
		} else {
			// Just a name - construct full path from current directory
			fullPath = strings.TrimSuffix(path, "/") + "/" + itemPath
		}

		isFolder := itemType == "folder" || itemType == "modelfolder"

		response.Items = append(response.Items, wsItem{
			Path:     fullPath,
			Name:     name,
			Type:     itemType,
			Size:     itemSize,
			IsFolder: isFolder,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleWorkspaceUpload handles file upload to BV-BRC workspace.
func (ui *UI) HandleWorkspaceUpload(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	caller := ui.getWorkspaceCaller(sess)
	if caller == nil {
		http.Error(w, `{"error": "Workspace not configured - please log in"}`, http.StatusServiceUnavailable)
		return
	}

	// Parse multipart form (max 100MB).
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, `{"error": "No file provided"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Get destination folder.
	destFolder := r.FormValue("folder")
	if destFolder == "" {
		destFolder = "/" + sess.Username + "/home"
	}

	// Read file content.
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
		return
	}

	filename := header.Filename
	destPath := strings.TrimSuffix(destFolder, "/") + "/" + filename

	// Determine object type based on extension.
	objType := "unspecified"
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".fasta") || strings.HasSuffix(lower, ".fa") || strings.HasSuffix(lower, ".fna"):
		objType = "contigs"
	case strings.HasSuffix(lower, ".fastq") || strings.HasSuffix(lower, ".fq"):
		objType = "reads"
	case strings.HasSuffix(lower, ".fastq.gz") || strings.HasSuffix(lower, ".fq.gz"):
		objType = "reads"
	case strings.HasSuffix(lower, ".gff") || strings.HasSuffix(lower, ".gff3"):
		objType = "gff"
	case strings.HasSuffix(lower, ".gbk") || strings.HasSuffix(lower, ".genbank"):
		objType = "genbank"
	case strings.HasSuffix(lower, ".csv"):
		objType = "csv"
	case strings.HasSuffix(lower, ".tsv") || strings.HasSuffix(lower, ".txt"):
		objType = "txt"
	}

	ui.logger.Info("uploading file to workspace",
		"filename", filename,
		"destPath", destPath,
		"size", len(data),
		"type", objType,
	)

	// For small files (< 10MB), use inline upload.
	// For larger files, we'd need to use Shock upload (createUploadNodes).
	const inlineLimit = 10 * 1024 * 1024 // 10MB

	if len(data) < inlineLimit {
		// Inline upload.
		result, err := caller.Call(r.Context(), "Workspace.create", []any{
			map[string]any{
				"objects": [][]any{
					{destPath, objType, nil, data},
				},
				"overwrite": true,
			},
		})
		if err != nil {
			ui.logger.Error("workspace upload failed", "path", destPath, "error", err)
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		ui.logger.Debug("workspace upload response", "result", string(result))
	} else {
		// Large file - need Shock upload.
		// First, create an upload node.
		result, err := caller.Call(r.Context(), "Workspace.create", []any{
			map[string]any{
				"objects": [][]any{
					{destPath, objType, nil, nil},
				},
				"createUploadNodes": true,
				"overwrite":         true,
			},
		})
		if err != nil {
			ui.logger.Error("workspace create upload node failed", "path", destPath, "error", err)
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}

		// Parse response to get Shock node ID.
		var createResp [][][]any
		if err := json.Unmarshal(result, &createResp); err != nil {
			ui.logger.Error("workspace parse upload node failed", "error", err)
			http.Error(w, `{"error": "Failed to parse upload node response"}`, http.StatusInternalServerError)
			return
		}

		if len(createResp) == 0 || len(createResp[0]) == 0 || len(createResp[0][0]) < 11 {
			http.Error(w, `{"error": "Invalid upload node response"}`, http.StatusInternalServerError)
			return
		}

		shockNodeID, _ := createResp[0][0][10].(string)
		if shockNodeID == "" {
			http.Error(w, `{"error": "No Shock node ID in response"}`, http.StatusInternalServerError)
			return
		}

		// Upload to Shock.
		shockURL := "https://p3.theseed.org/services/shock_api/node/" + shockNodeID
		if err := ui.uploadToShock(r.Context(), shockURL, filename, data, sess.Token); err != nil {
			ui.logger.Error("shock upload failed", "url", shockURL, "error", err)
			http.Error(w, fmt.Sprintf(`{"error": %q}`, err.Error()), http.StatusInternalServerError)
			return
		}
	}

	// Return success with the workspace path.
	response := struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}{
		Path: destPath,
		Name: filename,
		Type: objType,
		Size: int64(len(data)),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// uploadToShock uploads file data to a Shock node.
func (ui *UI) uploadToShock(ctx context.Context, shockURL, filename string, data []byte, token string) error {
	// Create multipart form.
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("upload", filename)
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, shockURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("shock upload failed (HTTP %d): %s", resp.StatusCode, body)
	}

	return nil
}

// HandleWorkspace renders the workspace browser.
func (ui *UI) HandleWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	caller := ui.getWorkspaceCaller(sess)
	if caller == nil {
		ui.renderError(w, "BV-BRC Workspace not configured - please log in", nil)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/" + sess.Username + "/home"
	}

	// Call workspace list using the Workspace service.
	ui.logger.Debug("workspace ls request", "path", path)
	result, err := caller.Call(r.Context(), "Workspace.ls", []any{
		map[string]any{"paths": []string{path}},
	})
	if err != nil {
		ui.logger.Error("workspace ls failed", "path", path, "error", err)
		ui.renderError(w, "Failed to list workspace", err)
		return
	}

	ui.logger.Debug("workspace ls response", "raw", string(result))

	// Response format: [{"/path": [[item], [item], ...]}]
	var outer []map[string]json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		ui.logger.Error("workspace parse failed", "error", err, "response", string(result))
		ui.renderError(w, "Failed to parse workspace response", err)
		return
	}

	// Extract items for the requested path.
	var items [][]any
	var foundKey string
	for key := range outer[0] {
		foundKey = key
		break
	}
	ui.logger.Debug("workspace response keys", "requestedPath", path, "foundKey", foundKey)

	if listing, ok := outer[0][path]; ok {
		json.Unmarshal(listing, &items)
	} else if listing, ok := outer[0][strings.TrimSuffix(path, "/")]; ok {
		json.Unmarshal(listing, &items)
	} else if len(outer[0]) > 0 {
		// Use whatever key is in the response
		for _, listing := range outer[0] {
			json.Unmarshal(listing, &items)
			break
		}
	}

	data := map[string]any{
		"Title":   "Workspace - GoWe",
		"Session": sess,
		"Path":    path,
		"Items":   items,
	}
	ui.render(w, "workspace/browser", data)
}

// --- Helper Methods ---

func (ui *UI) authenticateBVBRC(ctx context.Context, username, password string) (string, error) {
	// BV-BRC authentication endpoint.
	const authURL = "https://user.patricbrc.org/authenticate"

	// BV-BRC expects form-urlencoded data, not JSON.
	data := url.Values{}
	data.Set("username", username)
	data.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication failed: %s", resp.Status)
	}

	// The response is the token as plain text.
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", fmt.Errorf("empty token received")
	}

	return token, nil
}

func (ui *UI) isAdminUser(username string) bool {
	// TODO: Make this configurable via environment variable.
	adminUsers := []string{"admin", "gowe_admin"}
	for _, admin := range adminUsers {
		if strings.EqualFold(username, admin) {
			return true
		}
	}
	return false
}

func (ui *UI) parseListOptions(r *http.Request) model.ListOptions {
	opts := model.ListOptions{
		Limit:  20,
		Offset: 0,
	}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n <= 100 {
			opts.Limit = n
		}
	}

	if offset := r.URL.Query().Get("offset"); offset != "" {
		if n, err := strconv.Atoi(offset); err == nil && n >= 0 {
			opts.Offset = n
		}
	}

	if state := r.URL.Query().Get("state"); state != "" {
		opts.State = strings.ToUpper(state)
	}

	return opts
}

func (ui *UI) buildPagination(opts model.ListOptions, total int) map[string]any {
	hasMore := opts.Offset+opts.Limit < total
	hasPrev := opts.Offset > 0

	return map[string]any{
		"Total":      total,
		"Limit":      opts.Limit,
		"Offset":     opts.Offset,
		"HasMore":    hasMore,
		"HasPrev":    hasPrev,
		"NextOffset": opts.Offset + opts.Limit,
		"PrevOffset": max(0, opts.Offset-opts.Limit),
	}
}

func (ui *UI) pathParam(r *http.Request, name string) string {
	// Chi uses path value context.
	return r.PathValue(name)
}

func (ui *UI) render(w http.ResponseWriter, template string, data map[string]any) {
	// For now, render a simple HTML response.
	// This will be replaced with templ templates.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	var buf bytes.Buffer
	if err := renderTemplate(&buf, template, data); err != nil {
		ui.logger.Error("template render failed", "template", template, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}

func (ui *UI) renderError(w http.ResponseWriter, message string, err error) {
	ui.logger.Error(message, "error", err)
	data := map[string]any{
		"Title":   "Error - GoWe",
		"Message": message,
	}
	w.WriteHeader(http.StatusInternalServerError)
	ui.render(w, "error", data)
}

func (ui *UI) renderNotFound(w http.ResponseWriter, message string) {
	data := map[string]any{
		"Title":   "Not Found - GoWe",
		"Message": message,
	}
	w.WriteHeader(http.StatusNotFound)
	ui.render(w, "error", data)
}
