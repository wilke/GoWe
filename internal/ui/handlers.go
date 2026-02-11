package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// UI handles the web user interface.
type UI struct {
	store       store.Store
	sessions    *SessionManager
	logger      *slog.Logger
	bvbrcCaller bvbrc.RPCCaller
	startTime   time.Time
	secure      bool // Use secure cookies (HTTPS)
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

// WithBVBRCCaller sets the BV-BRC RPC caller for workspace operations.
func (ui *UI) WithBVBRCCaller(caller bvbrc.RPCCaller) {
	ui.bvbrcCaller = caller
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

	// Parse token to get expiry.
	tokenInfo := bvbrc.ParseToken(token)

	// Determine role (admin list can be configured via env or config).
	role := model.RoleUser
	if ui.isAdminUser(username) {
		role = model.RoleAdmin
	}

	// Create session.
	sess, err := ui.sessions.CreateSession(r.Context(), username, username, role, token, tokenInfo.Expiry)
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

	submissions, total, err := ui.store.ListSubmissions(r.Context(), opts)
	if err != nil {
		ui.renderError(w, "Failed to load submissions", err)
		return
	}

	// Get task summaries for each submission.
	for _, sub := range submissions {
		tasks, _ := ui.store.ListTasksBySubmission(r.Context(), sub.ID)
		taskList := make([]model.Task, len(tasks))
		for i, t := range tasks {
			taskList[i] = *t
		}
		sub.TaskSummary = model.ComputeTaskSummary(taskList)
	}

	data := map[string]any{
		"Title":       "Submissions - GoWe",
		"Session":     sess,
		"Submissions": submissions,
		"Pagination":  ui.buildPagination(opts, total),
		"StateFilter": opts.State,
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

	data := map[string]any{
		"Title":      fmt.Sprintf("Submission %s - GoWe", sub.ID),
		"Session":    sess,
		"Submission": sub,
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

	data := map[string]any{
		"Title":            "Submit Workflow - GoWe",
		"Session":          sess,
		"Workflows":        workflows,
		"SelectedWorkflow": selectedWorkflow,
		"Error":            r.URL.Query().Get("error"),
	}
	ui.render(w, "submissions/create", data)
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

// HandleWorkspace renders the workspace browser.
func (ui *UI) HandleWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	if ui.bvbrcCaller == nil {
		ui.renderError(w, "BV-BRC not configured", nil)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/" + sess.Username
	}

	// Call workspace list.
	result, err := ui.bvbrcCaller.Call(r.Context(), "Workspace.ls", []any{
		map[string]any{"paths": []string{path}},
	})
	if err != nil {
		ui.renderError(w, "Failed to list workspace", err)
		return
	}

	var items [][]any
	if err := json.Unmarshal(result, &items); err != nil {
		ui.renderError(w, "Failed to parse workspace response", err)
		return
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

	payload := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authentication failed: %s", resp.Status)
	}

	// The response is the token as plain text or JSON.
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
