package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/cancelseq"
	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	bvbrcpkg "github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/model"
)

// AdminChecker determines whether a username has admin privileges.
type AdminChecker interface {
	IsAdmin(username string) bool
}

// UI handles the web user interface.
type UI struct {
	store        store.Store
	sessions     *SessionManager
	logger       *slog.Logger
	bvbrcCaller  bvbrc.RPCCaller // AppService caller
	adminChecker AdminChecker    // Admin role checker (nil = no admins)
	startTime    time.Time

	// metrics is nil unless the server was started with --metrics-addr;
	// every Registry method no-ops on a nil receiver, so handler code never
	// checks for this.
	metrics *metrics.Registry

	// workspaceURL is the BV-BRC Workspace JSON-RPC endpoint. Every
	// workspace operation builds a per-request client against it with the
	// session's token; see workspaceClient.
	workspaceURL string
	// uploadMaxSize caps the request body of a workspace upload.
	uploadMaxSize int64

	// Session cookie hardening.
	secureCookies       bool // Always set the Secure attribute on session cookies
	trustForwardedProto bool // Honor X-Forwarded-Proto when deciding the Secure attribute

	// apiBaseURL overrides the base URL used for the admin outputs
	// loopback calls (see admin_outputs.go). Empty (the production default)
	// derives it per-request from the connection's local address; tests set
	// this to an httptest.Server URL instead.
	apiBaseURL string
}

// Config holds UI configuration.
type Config struct {
	// SecureCookies forces the Secure attribute on session cookies for every
	// request. Set this when TLS is terminated in-process, or by a trusted
	// upstream proxy that always speaks HTTPS to clients.
	SecureCookies bool
	// TrustForwardedProto enables honoring the X-Forwarded-Proto header to
	// decide the Secure attribute per-request. Enable only when the server sits
	// behind a trusted reverse proxy that sets this header, since a client can
	// otherwise spoof it.
	TrustForwardedProto bool
	// WorkspaceURL is the BV-BRC Workspace service endpoint used by the
	// workspace browser, the file picker and uploads. Empty selects
	// production.
	WorkspaceURL string
	// UploadMaxSize is the largest workspace upload request body accepted,
	// in bytes. Zero selects DefaultUploadMaxSize.
	UploadMaxSize int64
}

// DefaultUploadMaxSize is the workspace upload cap when none is configured.
const DefaultUploadMaxSize int64 = 1 << 30

// New creates a new UI handler.
func New(st store.Store, logger *slog.Logger, cfg Config) *UI {
	workspaceURL := cfg.WorkspaceURL
	if workspaceURL == "" {
		workspaceURL = bvbrcpkg.DefaultWorkspaceURL
	}
	uploadMaxSize := cfg.UploadMaxSize
	if uploadMaxSize <= 0 {
		uploadMaxSize = DefaultUploadMaxSize
	}
	return &UI{
		store:               st,
		sessions:            NewSessionManager(st),
		logger:              logger.With("component", "ui"),
		startTime:           time.Now(),
		secureCookies:       cfg.SecureCookies,
		trustForwardedProto: cfg.TrustForwardedProto,
		workspaceURL:        workspaceURL,
		uploadMaxSize:       uploadMaxSize,
	}
}

// resolveSecureCookie decides whether a session cookie should carry the Secure
// attribute for the given request. A cookie is marked Secure when any of the
// following hold:
//   - forceSecure is set (native TLS, or an operator opting in explicitly);
//   - the request reached the server over an in-process TLS connection;
//   - trustForwardedProto is enabled and the upstream proxy reports the
//     original client scheme as HTTPS via X-Forwarded-Proto.
func resolveSecureCookie(r *http.Request, forceSecure, trustForwardedProto bool) bool {
	if forceSecure {
		return true
	}
	if r.TLS != nil {
		return true
	}
	if trustForwardedProto && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

// secureCookie reports whether the session cookie for this request should be
// marked Secure, based on the server's configuration and the request scheme.
func (ui *UI) secureCookie(r *http.Request) bool {
	return resolveSecureCookie(r, ui.secureCookies, ui.trustForwardedProto)
}

// WithBVBRCCaller sets the BV-BRC RPC caller for AppService operations.
func (ui *UI) WithBVBRCCaller(caller bvbrc.RPCCaller) {
	ui.bvbrcCaller = caller
}

// WithAdminChecker sets the admin role checker.
func (ui *UI) WithAdminChecker(checker AdminChecker) {
	ui.adminChecker = checker
}

// WithMetrics sets the Prometheus metrics registry (nil is valid and leaves
// instrumentation disabled).
func (ui *UI) WithMetrics(m *metrics.Registry) {
	ui.metrics = m
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
	sess, err := ui.sessions.CreateSession(r.Context(), sessionUsername, sessionUsername, string(role), token, tokenInfo.Expiry)
	if err != nil {
		ui.logger.Error("create session failed", "error", err)
		http.Redirect(w, r, "/login?error=Session+creation+failed", http.StatusSeeOther)
		return
	}

	// Set session cookie.
	SetSessionCookie(w, sess, ui.secureCookie(r))

	ui.logger.Info("user logged in", "username", username, "session", sess.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleLogout clears the session and redirects to login.
func (ui *UI) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, _ := ui.sessions.GetSessionFromRequest(r); sess != nil {
		if err := ui.sessions.DeleteSession(r.Context(), sess.ID); err != nil {
			slog.Error("logout: failed to delete session", "session_id", sess.ID, "error", err)
		}
		ui.logger.Info("user logged out", "username", sess.Username, "session", sess.ID)
	}
	ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// HandleDashboard renders the main dashboard.
func (ui *UI) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	// Get workflow count and recent workflows.
	workflows, workflowCount, err := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 5})
	if err != nil {
		slog.Error("dashboard: failed to list workflows", "error", err)
	}

	// Get recent submissions (top-level only; scatter children would flood the list).
	submissions, _, err := ui.store.ListSubmissions(r.Context(), model.ListOptions{Limit: 5, ExcludeChildren: true})
	if err != nil {
		slog.Error("dashboard: failed to list submissions", "error", err)
	}

	// Count submissions by state for the last 24 hours.
	// Note: CountSubmissionsByState is global — counts include child submissions.
	since24h := time.Now().UTC().Add(-24 * time.Hour)
	stats24h, err := ui.store.CountSubmissionsByState(r.Context(), since24h, "")
	if err != nil {
		slog.Error("dashboard: failed to count submissions by state (24h)", "error", err)
	}

	// Count currently running (all time — running is a live state).
	allStats, err := ui.store.CountSubmissionsByState(r.Context(), time.Time{}, "")
	if err != nil {
		slog.Error("dashboard: failed to count submissions by state (all-time)", "error", err)
	}

	data := map[string]any{
		"Title":             "Dashboard - GoWe",
		"Session":           sess,
		"WorkflowCount":     workflowCount,
		"RecentWorkflows":   workflows,
		"RecentSubmissions": submissions,
		"Stats": map[string]int{
			"Running":   allStats["RUNNING"],
			"Completed": stats24h["COMPLETED"],
			"Failed":    stats24h["FAILED"],
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

	// Workflow-specific filters (search is already parsed by parseListOptions).
	opts.Class = r.URL.Query().Get("class")
	if labels := r.URL.Query()["label"]; len(labels) > 0 {
		opts.Labels = labels
	}

	workflows, total, err := ui.store.ListWorkflows(r.Context(), opts)
	if err != nil {
		ui.renderError(w, "Failed to load workflows", err)
		return
	}

	// Fetch CV for label filter pills.
	vocab, err := ui.store.ListLabelVocabulary(r.Context())
	if err != nil {
		slog.Error("failed to load label vocabulary", "error", err)
	}

	// Build active label filter set for highlighting.
	activeLabelSet := make(map[string]bool)
	for _, l := range opts.Labels {
		activeLabelSet[l] = true
	}

	// Base filter params (without sort) for column header links.
	filterBase := filterQuery(
		"search", opts.Search,
		"class", opts.Class,
	)
	for _, l := range opts.Labels {
		filterBase += filterQuery("label", l)
	}
	// Full filter params (with sort) for pagination links.
	filterParams := filterBase + filterQuery(
		"sort", opts.SortBy,
		"dir", opts.SortDir,
	)

	data := map[string]any{
		"Title":        "Workflows - GoWe",
		"Session":      sess,
		"Workflows":    workflows,
		"Pagination":   ui.buildPagination(opts, total, len(workflows), filterParams, []int{10, 20, 50, 100}),
		"SearchQuery":  opts.Search,
		"ClassFilter":  opts.Class,
		"LabelFilters": opts.Labels,
		"ActiveLabels": activeLabelSet,
		"LabelVocab":   vocab,
		"SortBy":       opts.SortBy,
		"SortDir":      opts.SortDir,
		"FilterBase":   filterBase,
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

// HandleWorkflowEdit renders the workflow edit form.
func (ui *UI) HandleWorkflowEdit(w http.ResponseWriter, r *http.Request) {
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

	// The edit form exposes the raw CWL and leads to a mutation; restrict it to
	// the owner or an admin, matching handleUpdateWorkflow's save-side check.
	// (Legacy workflows with no recorded owner remain editable.)
	if sess != nil && !sess.IsAdmin() && wf.CreatedBy != "" && wf.CreatedBy != sess.Username {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	data := map[string]any{
		"Title":    "Edit " + wf.Name + " - GoWe",
		"Session":  sess,
		"Workflow": wf,
	}
	ui.render(w, "workflows/edit", data)
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

	// Non-admin users only see their own submissions.
	opts := ui.parseListOptions(r)
	if sess != nil && !sess.IsAdmin() {
		opts.SubmittedBy = sess.Username
	}
	// Hide child submissions (scatter/sub-workflow fan-out) from the list view.
	opts.ExcludeChildren = true

	// View mode: "cards" (default) or "table".
	viewMode := r.URL.Query().Get("view")
	if viewMode != "table" {
		viewMode = "cards"
	}
	// Default page sizes: 9 for cards (3x3 grid), 20 for table.
	if r.URL.Query().Get("limit") == "" {
		if viewMode == "table" {
			opts.Limit = 20
		} else {
			opts.Limit = 9
		}
	}

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
		tasks, err := ui.store.ListTasksBySubmission(r.Context(), sub.ID)
		if err != nil {
			slog.Error("submission list: failed to list tasks", "submission_id", sub.ID, "error", err)
		}
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

	// Page sizes depend on view mode.
	pageSizes := []int{10, 20, 50, 100}
	if viewMode == "cards" {
		pageSizes = []int{9, 18, 36}
	}

	// Base filter params (without sort) for column header links.
	filterBase := filterQuery(
		"state", opts.State,
		"search", opts.Search,
		"date_start", dateStart,
		"date_end", dateEnd,
		"view", viewMode,
	)
	// Full filter params (with sort) for pagination links.
	filterParams := filterBase + filterQuery(
		"sort", opts.SortBy,
		"dir", opts.SortDir,
	)

	data := map[string]any{
		"Title":       "Submissions - GoWe",
		"Session":     sess,
		"Submissions": submissions,
		"Pagination":  ui.buildPagination(opts, total, len(submissions), filterParams, pageSizes),
		"StateFilter": opts.State,
		"SearchQuery": opts.Search,
		"DateStart":   dateStart,
		"DateEnd":     dateEnd,
		"ViewMode":    viewMode,
		"SortBy":      opts.SortBy,
		"SortDir":     opts.SortDir,
		"FilterBase":  filterBase,
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

	// Ownership check: non-admin users can only view their own submissions.
	if sess != nil && !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Compute task summary
	sub.TaskSummary = model.ComputeTaskSummary(sub.Tasks)

	// Calculate queue position if pending. Child submissions (scatter items)
	// are excluded from the pending list this searches, so a position would
	// never match — skip them; their scheduling is driven by the parent.
	if sub.State == model.SubmissionStatePending && sub.ParentTaskID == "" {
		pendingSubs, _, err := ui.store.ListSubmissions(r.Context(), model.ListOptions{
			State:           "PENDING",
			Limit:           1000,
			ExcludeChildren: true, // queue position counts top-level submissions only
		})
		if err != nil {
			slog.Error("submission detail: failed to list pending submissions for queue position", "error", err)
		}
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
		workflow, err = ui.store.GetWorkflow(r.Context(), sub.WorkflowID)
		if err != nil {
			slog.Error("submission detail: failed to load workflow for DAG", "workflow_id", sub.WorkflowID, "error", err)
		}
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
	workflows, _, err := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 100})
	if err != nil {
		slog.Error("submission create: failed to list workflows", "error", err)
	}

	// Pre-select workflow if ID provided.
	workflowID := r.URL.Query().Get("workflow_id")
	var selectedWorkflow *model.Workflow
	if workflowID != "" {
		selectedWorkflow, err = ui.store.GetWorkflow(r.Context(), workflowID)
		if err != nil {
			slog.Error("submission create: failed to get selected workflow", "workflow_id", workflowID, "error", err)
		}
	}

	// Build workspace path for file picker.
	// Workspace is available if user has a session with a token (even if no global workspace caller).
	workspacePath := ""
	hasWorkspace := false
	if sess != nil && sess.Username != "" && sess.Token != "" {
		workspacePath = "/" + sess.Username + "/home"
		hasWorkspace = true
	}

	// Fetch worker groups for admin users (dropdown), non-admins type freely.
	isAdmin := sess != nil && sess.IsAdmin()
	var workerGroups []string
	if isAdmin {
		workerGroups, err = ui.store.ListWorkerGroups(r.Context())
		if err != nil {
			slog.Error("submission create: failed to list worker groups", "error", err)
		}
	}

	data := map[string]any{
		"Title":            "Submit Workflow - GoWe",
		"Session":          sess,
		"Workflows":        workflows,
		"SelectedWorkflow": selectedWorkflow,
		"WorkspacePath":    workspacePath,
		"HasWorkspace":     hasWorkspace,
		"WorkerGroups":     workerGroups,
		"IsAdmin":          isAdmin,
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

	// Set worker group label if provided (mirrors CLI --group behavior).
	if workerGroup := strings.TrimSpace(r.FormValue("worker_group")); workerGroup != "" {
		labels["worker_group"] = workerGroup
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
		sub.UserToken = sess.Token
		if !sess.TokenExp.IsZero() {
			sub.TokenExpiry = sess.TokenExp
		}
		sub.AuthProvider = "bvbrc"
	}

	// Output destination from form (optional).
	if dest := r.FormValue("output_destination"); dest != "" {
		sub.OutputDestination = dest
	}

	if err := ui.store.CreateSubmission(r.Context(), sub); err != nil {
		ui.logger.Error("create submission failed", "error", err)
		http.Redirect(w, r, "/submissions/new?workflow_id="+workflowID+"&error=Failed+to+create+submission", http.StatusSeeOther)
		return
	}

	// Create StepInstances for each workflow step (3-level state architecture).
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
	if err := ui.store.BatchCreateStepInstances(r.Context(), stepInstances); err != nil {
		ui.logger.Error("batch create step instances failed", "error", err, "submission_id", sub.ID)
		// Submission with zero steps is an orphan the scheduler can't progress — treat as hard failure.
		http.Redirect(w, r, "/submissions/new?workflow_id="+workflowID+"&error=Failed+to+initialize+submission", http.StatusSeeOther)
		return
	}

	ui.logger.Info("submission created via UI", "id", sub.ID, "workflow", wf.Name, "user", sub.SubmittedBy)
	http.Redirect(w, r, "/submissions/"+sub.ID, http.StatusSeeOther)
}

// HandleSubmissionCancel cancels a running submission (HTMX).
//
// This runs the same cancel sequence as the API handler (internal/server),
// shared via internal/cancelseq: cancel non-terminal steps/tasks, fan out
// synchronously to sub-workflow child submissions, and observe metrics.
// Before internal/cancelseq existed, this handler finalized the submission
// directly and did none of that — a UI-initiated cancel left tasks running
// and workers never learned about it (issue #185).
func (ui *UI) HandleSubmissionCancel(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Ownership check: non-admin users can only cancel their own submissions.
	if sess != nil && !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if sub.State.IsTerminal() {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sub.State = model.SubmissionStateCancelled
	now := time.Now().UTC()
	sub.CompletedAt = &now

	if _, err := cancelseq.Run(r.Context(), ui.store, ui.metrics, ui.logger, sub, now); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Redirect to refresh the page.
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
}

// HandleSubmissionDelete deletes a submission and all its tasks/steps (HTMX).
func (ui *UI) HandleSubmissionDelete(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Deletion is permanent and cascading; require an authenticated session so an
	// unauthenticated/anonymous caller cannot delete submissions. Non-admins may
	// delete only their own.
	if sess == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	// Cancel any in-flight work before deleting rows so workers see the
	// cancellation on their next poll rather than reporting to a missing task.
	now := time.Now().UTC()
	ui.store.CancelNonTerminalSteps(r.Context(), id, now)
	ui.store.CancelNonTerminalTasks(r.Context(), id, now)

	if err := ui.store.DeleteSubmission(r.Context(), id); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Redirect to the submission list after deletion.
	w.Header().Set("HX-Redirect", "/submissions")
	w.WriteHeader(http.StatusOK)
}

// HandleTaskLogs renders task logs (stdout/stderr).
func (ui *UI) HandleTaskLogs(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	subID := ui.pathParam(r, "id")
	taskID := ui.pathParam(r, "tid")

	// Ownership check: verify the caller can access the parent submission.
	sub, subErr := ui.store.GetSubmission(r.Context(), subID)
	if subErr == nil && sub != nil {
		if sess != nil && !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

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
	sess := SessionFromContext(r.Context())
	opts := ui.parseListOptions(r)
	opts.Limit = 10000 // Export up to 10k records
	// Export is deliberately global: it includes child submissions (scatter
	// items), unlike the list view, so exported data is complete.

	// Non-admin users only export their own submissions.
	if sess != nil && !sess.IsAdmin() {
		opts.SubmittedBy = sess.Username
	}

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
		tasks, err := ui.store.ListTasksBySubmission(r.Context(), sub.ID)
		if err != nil {
			slog.Error("submission export: failed to list tasks", "submission_id", sub.ID, "error", err)
		}
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

// resetFailedTaskForQueue prepares a FAILED task snapshot for re-execution
// (QUEUED, cleared retry/output bookkeeping) and writes it back only while
// the task is still exactly FAILED in the store — a guarded full-row write
// in the same style as CASTaskState, reusing the exact-state primitive
// TerminalizeTaskFrom (see its stuck-task caller in
// internal/scheduler/loop.go) instead of an unconditional UpdateTask. A
// concurrent scheduler write (markRetries' FAILED→RETRYING CAS, or a cancel
// SKIP) between the caller's read and this write makes the guard reject the
// write (applied=false, no error) instead of the stale UI snapshot
// clobbering the winning state — the caller must skip/count/report that
// task rather than treat it as reset.
func (ui *UI) resetFailedTaskForQueue(ctx context.Context, task model.Task) (bool, error) {
	task.State = model.TaskStateQueued
	task.RetryCount = 0
	task.Stdout = ""
	task.Stderr = ""
	task.ExitCode = nil
	task.StartedAt = nil
	task.CompletedAt = nil
	return ui.store.TerminalizeTaskFrom(ctx, &task, model.TaskStateFailed)
}

// HandleSubmissionResume resumes a failed submission.
func (ui *UI) HandleSubmissionResume(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Ownership check: non-admin users can only resume their own submissions.
	if sess != nil && !sess.IsAdmin() && sub.SubmittedBy != sess.Username {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if sub.State != model.SubmissionStateFailed {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Reset failed tasks to QUEUED and their parent step instances to DISPATCHED.
	requeuedCount := 0
	skippedCount := 0
	resetSteps := map[string]bool{}
	for _, task := range sub.Tasks {
		if task.State != model.TaskStateFailed {
			continue
		}
		applied, err := ui.resetFailedTaskForQueue(r.Context(), task)
		if err != nil {
			ui.logger.Error("failed to reset task", "task_id", task.ID, "error", err)
			continue
		}
		if !applied {
			skippedCount++
			ui.logger.Info("resume: task no longer FAILED, skipping", "task_id", task.ID, "submission_id", id)
			continue
		}
		requeuedCount++
		if task.StepInstanceID != "" {
			resetSteps[task.StepInstanceID] = true
		}
	}
	// Reset parent step instances so scheduler re-evaluates them.
	for siID := range resetSteps {
		si, err := ui.store.GetStepInstance(r.Context(), siID)
		if err != nil || si == nil {
			continue
		}
		si.State = model.StepStateDispatched
		si.CompletedAt = nil
		if err := ui.store.UpdateStepInstance(r.Context(), si); err != nil {
			ui.logger.Error("failed to reset step instance", "si_id", siID, "error", err)
		}
	}

	// Set submission back to RUNNING.
	sub.State = model.SubmissionStateRunning
	sub.CompletedAt = nil
	if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	ui.logger.Info("submission resumed", "id", id, "requeued", requeuedCount, "skipped", skippedCount)
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
	// HTMX discards the body when HX-Redirect is set (it drives a full-page
	// navigation instead), so this is purely for API/test observability of
	// any CAS misses skipped above.
	fmt.Fprintf(w, "requeued %d, skipped %d", requeuedCount, skippedCount)
}

// HandleRecomputeFailed recomputes all failed tasks in a submission.
func (ui *UI) HandleRecomputeFailed(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	sub, err := ui.store.GetSubmission(r.Context(), id)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Gate on submission state: a cancelled submission is not actionable —
	// re-queuing its failed tasks would resurrect work the user explicitly
	// tore down (mirrors the cancel handler's terminal-state gate).
	if sub.State == model.SubmissionStateCancelled {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Reset all failed tasks and their parent step instances.
	recomputeCount := 0
	skippedCount := 0
	resetSteps := map[string]bool{}
	for _, task := range sub.Tasks {
		if task.State != model.TaskStateFailed {
			continue
		}
		applied, err := ui.resetFailedTaskForQueue(r.Context(), task)
		if err != nil {
			ui.logger.Error("failed to reset task", "task_id", task.ID, "error", err)
			continue
		}
		if !applied {
			skippedCount++
			ui.logger.Info("recompute: task no longer FAILED, skipping", "task_id", task.ID, "submission_id", id)
			continue
		}
		recomputeCount++
		if task.StepInstanceID != "" {
			resetSteps[task.StepInstanceID] = true
		}
	}
	for siID := range resetSteps {
		si, err := ui.store.GetStepInstance(r.Context(), siID)
		if err != nil || si == nil {
			continue
		}
		si.State = model.StepStateDispatched
		si.CompletedAt = nil
		if err := ui.store.UpdateStepInstance(r.Context(), si); err != nil {
			ui.logger.Error("failed to reset step instance", "si_id", siID, "error", err)
		}
	}

	// If submission was terminal, set it back to RUNNING.
	if sub.State.IsTerminal() && recomputeCount > 0 {
		sub.State = model.SubmissionStateRunning
		sub.CompletedAt = nil
		if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
			ui.logger.Error("failed to update submission", "id", id, "error", err)
		}
	}

	ui.logger.Info("recomputed failed tasks", "id", id, "count", recomputeCount, "skipped", skippedCount)
	w.Header().Set("HX-Redirect", "/submissions/"+id)
	w.WriteHeader(http.StatusOK)
	// HTMX discards the body when HX-Redirect is set (it drives a full-page
	// navigation instead), so this is purely for API/test observability of
	// any CAS misses skipped above.
	fmt.Fprintf(w, "recomputed %d, skipped %d", recomputeCount, skippedCount)
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

	// Gate on submission state before writing: a cancelled submission is not
	// actionable (mirrors the cancel handler's terminal-state gate). Fetched
	// up front so the CANCELLED check happens before any task write.
	sub, err := ui.store.GetSubmission(r.Context(), subID)
	if err != nil || sub == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if sub.State == model.SubmissionStateCancelled {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Reset the task via a guarded write: only applies while the task is
	// still exactly FAILED. A concurrent scheduler write (e.g. markRetries'
	// FAILED→RETRYING CAS winning the race against this handler's read
	// above) makes the guard reject the write instead of this stale
	// snapshot resurrecting/double-executing a task the scheduler already
	// claimed.
	applied, err := ui.resetFailedTaskForQueue(r.Context(), *task)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !applied {
		ui.logger.Info("task recompute: task no longer FAILED, skipping", "task_id", taskID, "submission_id", subID)
		w.WriteHeader(http.StatusConflict)
		fmt.Fprintf(w, "task %s is no longer FAILED, skipped", taskID)
		return
	}

	// Reset parent step instance so scheduler re-evaluates it.
	if task.StepInstanceID != "" {
		si, err := ui.store.GetStepInstance(r.Context(), task.StepInstanceID)
		if err == nil && si != nil {
			si.State = model.StepStateDispatched
			si.CompletedAt = nil
			if err := ui.store.UpdateStepInstance(r.Context(), si); err != nil {
				ui.logger.Error("failed to reset step instance", "si_id", task.StepInstanceID, "error", err)
			}
		}
	}

	// Set submission back to RUNNING if it was terminal.
	if sub.State.IsTerminal() {
		sub.State = model.SubmissionStateRunning
		sub.CompletedAt = nil
		if err := ui.store.UpdateSubmission(r.Context(), sub); err != nil {
			ui.logger.Error("failed to update submission", "id", subID, "error", err)
		}
	}

	ui.logger.Info("task recomputed", "task_id", taskID, "submission_id", subID)
	w.Header().Set("HX-Redirect", "/submissions/"+subID)
	w.WriteHeader(http.StatusOK)
}

// --- Worker Handlers ---

// HandleWorkerList renders the worker status page.
func (ui *UI) HandleWorkerList(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	allWorkers, err := ui.store.ListWorkers(r.Context())
	if err != nil {
		ui.renderError(w, "Failed to load workers", err)
		return
	}

	// Parse filters.
	searchQuery := strings.TrimSpace(r.URL.Query().Get("search"))
	stateFilter := strings.ToLower(r.URL.Query().Get("state"))

	// Count offline workers (from all workers, pre-filter) for the purge button.
	offlineCount := 0
	for _, wk := range allWorkers {
		if wk.State == model.WorkerStateOffline {
			offlineCount++
		}
	}

	// Filter workers in memory.
	var workers []*model.Worker
	searchLower := strings.ToLower(searchQuery)
	for _, wk := range allWorkers {
		// State filter.
		if stateFilter != "" && strings.ToLower(string(wk.State)) != stateFilter {
			continue
		}
		// Search filter (name, hostname, group, ID).
		if searchQuery != "" {
			if !strings.Contains(strings.ToLower(wk.Name), searchLower) &&
				!strings.Contains(strings.ToLower(wk.Hostname), searchLower) &&
				!strings.Contains(strings.ToLower(wk.Group), searchLower) &&
				!strings.Contains(strings.ToLower(wk.ID), searchLower) {
				continue
			}
		}
		workers = append(workers, wk)
	}

	// Build task→submission map for linking current tasks.
	taskSubmission := map[string]string{}
	for _, wk := range workers {
		if wk.CurrentTask != "" {
			if t, err := ui.store.GetTask(r.Context(), wk.CurrentTask); err == nil && t != nil {
				taskSubmission[wk.CurrentTask] = t.SubmissionID
			}
		}
	}

	data := map[string]any{
		"Title":          "Workers - GoWe",
		"Session":        sess,
		"Workers":        workers,
		"AllCount":       len(allWorkers),
		"OfflineCount":   offlineCount,
		"TaskSubmission": taskSubmission,
		"SearchQuery":    searchQuery,
		"StateFilter":    stateFilter,
	}
	ui.render(w, "workers", data)
}

// HandleWorkerDelete removes a single worker (HTMX).
func (ui *UI) HandleWorkerDelete(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	if err := ui.store.DeleteWorker(r.Context(), id); err != nil {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	ui.logger.Info("worker deleted via UI", "id", id)
	w.WriteHeader(http.StatusOK)
}

// HandleWorkerPurgeOffline deletes all offline workers (HTMX).
func (ui *UI) HandleWorkerPurgeOffline(w http.ResponseWriter, r *http.Request) {
	workers, err := ui.store.ListWorkers(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	count := 0
	for _, wk := range workers {
		if wk.State == model.WorkerStateOffline {
			if err := ui.store.DeleteWorker(r.Context(), wk.ID); err == nil {
				count++
			}
		}
	}

	ui.logger.Info("purged offline workers via UI", "count", count)
	w.Header().Set("HX-Redirect", "/workers")
	w.WriteHeader(http.StatusOK)
}

// --- Admin Handlers ---

// HandleAdminStats renders system statistics.
func (ui *UI) HandleAdminStats(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	// Get counts.
	_, workflowCount, err := ui.store.ListWorkflows(r.Context(), model.ListOptions{Limit: 1})
	if err != nil {
		slog.Error("admin stats: failed to count workflows", "error", err)
	}
	_, submissionCount, err := ui.store.ListSubmissions(r.Context(), model.ListOptions{Limit: 1})
	if err != nil {
		slog.Error("admin stats: failed to count submissions", "error", err)
	}

	// All-time stats (for Running/Pending which are live states).
	// Note: CountSubmissionsByState is global — counts include child submissions.
	allStats, err := ui.store.CountSubmissionsByState(r.Context(), time.Time{}, "")
	if err != nil {
		slog.Error("admin stats: failed to count submissions by state", "error", err)
	}

	// Time-period breakdowns.
	now := time.Now().UTC()
	periods := []struct {
		Label string
		Since time.Time
	}{
		{"Hour", now.Add(-1 * time.Hour)},
		{"Day", now.Add(-24 * time.Hour)},
		{"Week", now.Add(-7 * 24 * time.Hour)},
		{"Month", now.Add(-30 * 24 * time.Hour)},
		{"Year", now.Add(-365 * 24 * time.Hour)},
		{"All", time.Time{}},
	}

	type periodStats struct {
		Label     string
		Running   int
		Completed int
		Failed    int
		Cancelled int
		Total     int
	}

	var breakdown []periodStats
	for _, p := range periods {
		counts, err := ui.store.CountSubmissionsByState(r.Context(), p.Since, "")
		if err != nil {
			slog.Error("admin stats: failed to count submissions by state for period", "period", p.Label, "error", err)
		}
		total := 0
		for _, c := range counts {
			total += c
		}
		breakdown = append(breakdown, periodStats{
			Label:     p.Label,
			Running:   counts["RUNNING"],
			Completed: counts["COMPLETED"],
			Failed:    counts["FAILED"],
			Cancelled: counts["CANCELLED"],
			Total:     total,
		})
	}

	data := map[string]any{
		"Title":           "System Stats - GoWe",
		"Session":         sess,
		"WorkflowCount":   workflowCount,
		"SubmissionCount": submissionCount,
		"SubmissionStats": map[string]int{
			"Pending":   allStats["PENDING"],
			"Running":   allStats["RUNNING"],
			"Completed": allStats["COMPLETED"],
			"Failed":    allStats["FAILED"],
		},
		"Breakdown": breakdown,
		"Uptime":    time.Since(ui.startTime).Round(time.Second).String(),
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

// HandleAdminActiveTasks renders the active tasks dashboard.
func (ui *UI) HandleAdminActiveTasks(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	active, err := ui.store.GetActiveTasks(r.Context())
	if err != nil {
		slog.Error("admin tasks: failed to get active tasks", "error", err)
	}

	// Load submission info for each task.
	subCache := make(map[string]*model.Submission)
	for _, t := range active {
		if _, ok := subCache[t.SubmissionID]; !ok {
			sub, err := ui.store.GetSubmission(r.Context(), t.SubmissionID)
			if err == nil && sub != nil {
				subCache[t.SubmissionID] = sub
			}
		}
	}

	data := map[string]any{
		"Title":       "Active Tasks - GoWe",
		"Session":     sess,
		"Tasks":       active,
		"Submissions": subCache,
		"Total":       len(active),
	}
	ui.render(w, "admin/tasks", data)
}

// --- Admin Label Vocabulary Handlers ---

// HandleAdminLabels renders the label vocabulary management page.
func (ui *UI) HandleAdminLabels(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	entries, err := ui.store.ListLabelVocabulary(r.Context())
	if err != nil {
		ui.renderError(w, "Failed to load label vocabulary", err)
		return
	}

	data := map[string]any{
		"Title":   "Label Vocabulary - GoWe",
		"Session": sess,
		"Entries": entries,
	}
	ui.render(w, "admin/labels", data)
}

// HandleAdminLabelCreate creates a new CV entry (form POST).
func (ui *UI) HandleAdminLabelCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	key := strings.TrimSpace(r.FormValue("key"))
	value := strings.TrimSpace(r.FormValue("value"))
	if key == "" || value == "" {
		http.Redirect(w, r, "/admin/labels?error=Key+and+value+are+required", http.StatusSeeOther)
		return
	}

	lv := &model.LabelVocabulary{
		ID:          "lv_" + uuid.New().String(),
		Key:         key,
		Value:       value,
		Description: strings.TrimSpace(r.FormValue("description")),
		Color:       r.FormValue("color"),
		CreatedAt:   time.Now().UTC(),
	}

	if err := ui.store.CreateLabelVocabulary(r.Context(), lv); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			qv := url.Values{}
			qv.Set("error", fmt.Sprintf("Entry already exists for %s:%s", key, value))
			http.Redirect(w, r, "/admin/labels?"+qv.Encode(), http.StatusSeeOther)
			return
		}
		ui.renderError(w, "Failed to create label", err)
		return
	}

	http.Redirect(w, r, "/admin/labels", http.StatusSeeOther)
}

// HandleAdminLabelDelete deletes a CV entry (HTMX DELETE).
func (ui *UI) HandleAdminLabelDelete(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	if err := ui.store.DeleteLabelVocabulary(r.Context(), id); err != nil {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// --- Workspace Handlers ---

// Workspace client settings for user-facing operations. Listings and the
// JSON-RPC halves of an upload are short calls; the Shock PUT itself is
// carried by the client's dedicated upload transport and bounded only by the
// request context.
const (
	workspaceRPCTimeout    = 60 * time.Second
	workspaceRPCRetries    = 3
	workspaceUploadRetries = 3

	// workspaceUploadMemory is the in-memory budget for parsing an upload
	// form; parts beyond it spill to temporary files instead of RAM.
	workspaceUploadMemory = 32 << 20
)

// errNoWorkspaceSession is returned when a workspace operation has no BV-BRC
// token to act under.
var errNoWorkspaceSession = errors.New("workspace access requires a BV-BRC login")

// workspaceClient returns a typed Workspace client acting as the session's
// user. Workspace operations always run under the SESSION token: the object
// is created, PUT to Shock, and refreshed as the same identity, so ACLs on the
// Workspace row and on the Shock node agree. There is deliberately no
// service-account fallback — an object created by the service account inside
// a user's home but written with the user's token is exactly the ACL mismatch
// this replaced — so a session without a token cannot use the workspace.
func (ui *UI) workspaceClient(sess *model.Session) (*bvbrcpkg.Client, error) {
	if sess == nil || sess.Token == "" {
		return nil, errNoWorkspaceSession
	}
	return bvbrcpkg.NewClient(bvbrcpkg.Config{
		WorkspaceURL: ui.workspaceURL,
		Token:        sess.Token,
		Timeout:      workspaceRPCTimeout,
		MaxRetries:   workspaceRPCRetries,
		RetryDelay:   bvbrcpkg.DefaultRetryDelay,
	}, ui.logger), nil
}

// workspaceHome is the default browsing root for a session.
func workspaceHome(sess *model.Session) string {
	return "/" + sess.Username + "/home"
}

// listWorkspaceDir lists one workspace directory and returns its entries. The
// service keys its reply by the path it was given; tolerate a trailing slash
// on either side.
func listWorkspaceDir(ctx context.Context, client *bvbrcpkg.Client, dir string) ([]bvbrcpkg.WorkspaceObject, error) {
	result, err := client.WorkspaceLs(ctx, bvbrcpkg.WorkspaceLsInput{Paths: []string{dir}})
	if err != nil {
		return nil, err
	}

	if items, ok := result[dir]; ok {
		return items, nil
	}
	trimmed := strings.TrimSuffix(dir, "/")
	if items, ok := result[trimmed]; ok {
		return items, nil
	}
	if items, ok := result[trimmed+"/"]; ok {
		return items, nil
	}
	if len(result) == 0 {
		// The service answers with an empty map for a directory it knows
		// but has nothing to report for; that is an empty listing.
		return []bvbrcpkg.WorkspaceObject{}, nil
	}
	if len(result) == 1 {
		// Single-path request: whatever key the service used is our listing.
		for _, items := range result {
			return items, nil
		}
	}
	return nil, fmt.Errorf("workspace listing for path %q not found in response", dir)
}

// writeJSONError writes a JSON error body the UI's JavaScript understands.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// HandleWorkspaceAPI returns workspace listing as JSON (for file picker modal).
func (ui *UI) HandleWorkspaceAPI(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	client, err := ui.workspaceClient(sess)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}

	wsPath := r.URL.Query().Get("path")
	if wsPath == "" {
		wsPath = workspaceHome(sess)
	}

	items, err := listWorkspaceDir(r.Context(), client, wsPath)
	if err != nil {
		ui.logger.Error("workspace API ls failed", "path", wsPath, "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

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
		Path:  wsPath,
		Items: make([]wsItem, 0, len(items)),
	}

	for _, obj := range items {
		response.Items = append(response.Items, wsItem{
			Path:     obj.Path,
			Name:     obj.Name,
			Type:     string(obj.Type),
			Size:     obj.Size,
			IsFolder: isWorkspaceFolder(obj.Type),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// isWorkspaceFolder reports whether an object type is browsed as a directory.
func isWorkspaceFolder(t bvbrcpkg.WorkspaceObjectType) bool {
	return t == bvbrcpkg.WorkspaceTypeFolder || t == "modelfolder"
}

// workspaceObjectTypeFor picks the Workspace object type from a file name.
func workspaceObjectTypeFor(filename string) bvbrcpkg.WorkspaceObjectType {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".fasta") || strings.HasSuffix(lower, ".fa") || strings.HasSuffix(lower, ".fna"):
		return "contigs"
	case strings.HasSuffix(lower, ".fastq") || strings.HasSuffix(lower, ".fq"):
		return "reads"
	case strings.HasSuffix(lower, ".fastq.gz") || strings.HasSuffix(lower, ".fq.gz"):
		return "reads"
	case strings.HasSuffix(lower, ".gff") || strings.HasSuffix(lower, ".gff3"):
		return "gff"
	case strings.HasSuffix(lower, ".gbk") || strings.HasSuffix(lower, ".genbank"):
		return "genbank"
	case strings.HasSuffix(lower, ".csv"):
		return "csv"
	case strings.HasSuffix(lower, ".tsv") || strings.HasSuffix(lower, ".txt"):
		return "txt"
	}
	return bvbrcpkg.WorkspaceTypeUnspecified
}

// HandleWorkspaceUpload handles file upload to BV-BRC workspace.
//
// The upload form is parsed with a bounded memory budget — larger parts spill
// to a temporary file — and the part is streamed to the workspace under the
// session's token with an exact Content-Length. Every attempt rewinds the part
// and starts afresh, because WorkspaceUploadReader verifies what the service
// stored and cleans up after itself on failure.
func (ui *UI) HandleWorkspaceUpload(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	client, err := ui.workspaceClient(sess)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}

	// Cap the whole request body; the cap surfaces as *http.MaxBytesError
	// from the form parser.
	r.Body = http.MaxBytesReader(w, r.Body, ui.uploadMaxSize)
	if err := r.ParseMultipartForm(workspaceUploadMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload exceeds the %d byte limit", ui.uploadMaxSize))
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "No file provided")
		return
	}
	defer file.Close()

	// Browsers send a bare name, but never trust it: the object name must not
	// carry directory components.
	filename := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	switch filename {
	case "", ".", "..", "/":
		writeJSONError(w, http.StatusBadRequest, "Invalid file name")
		return
	}
	if strings.ContainsFunc(filename, unicode.IsControl) {
		writeJSONError(w, http.StatusBadRequest, "Invalid file name: control characters are not allowed")
		return
	}

	destFolder := r.FormValue("folder")
	if destFolder == "" {
		destFolder = workspaceHome(sess)
	}
	destPath := strings.TrimSuffix(destFolder, "/") + "/" + filename
	objType := workspaceObjectTypeFor(filename)

	ui.logger.Info("uploading file to workspace",
		"filename", filename,
		"destPath", destPath,
		"size", header.Size,
		"type", objType,
		"user", sess.Username,
	)

	obj, err := uploadWorkspaceFile(r.Context(), client, destPath, filename, file, header.Size, objType)
	if err != nil {
		ui.logger.Error("workspace upload failed", "path", destPath, "error", err)
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	response := struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}{
		Path: destPath,
		Name: filename,
		Type: string(objType),
		Size: obj.Size,
	}
	if obj.Path != "" {
		response.Path = obj.Path
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// uploadWorkspaceFile streams file into the workspace object at wsPath,
// rewinding the reader for every attempt.
func uploadWorkspaceFile(ctx context.Context, client *bvbrcpkg.Client, wsPath, filename string, file io.ReadSeeker, size int64, objType bvbrcpkg.WorkspaceObjectType) (*bvbrcpkg.WorkspaceObject, error) {
	var lastErr error
	for attempt := 0; attempt < workspaceUploadRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewinding upload: %w", err)
		}

		obj, err := client.WorkspaceUploadReader(ctx, wsPath, filename, file, size, objType)
		if err == nil {
			return obj, nil
		}
		lastErr = err
		if ctx.Err() != nil || permanentUploadError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", workspaceUploadRetries, lastErr)
}

// permanentUploadError reports whether an upload failure is deterministic —
// an auth/permission refusal or a coded service error the client library
// itself would not retry — so another attempt cannot help. The gate is
// deliberately narrow: WorkspaceUploadReader wraps its verification failures
// (size and md5 mismatches, a bad Shock reply) in a code-less Error, and
// those must still be retried.
func permanentUploadError(err error) bool {
	if bvbrcpkg.IsAuthError(err) {
		return true
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if be, ok := e.(*bvbrcpkg.Error); ok && be.Code != 0 && !bvbrcpkg.IsRetryable(be) {
			return true
		}
	}
	return false
}

// HandleWorkspace renders the workspace browser.
func (ui *UI) HandleWorkspace(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	client, err := ui.workspaceClient(sess)
	if err != nil {
		// The browser page needs a BV-BRC login; send the user there.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	wsPath := r.URL.Query().Get("path")
	if wsPath == "" {
		wsPath = workspaceHome(sess)
	}

	ui.logger.Debug("workspace ls request", "path", wsPath)
	items, err := listWorkspaceDir(r.Context(), client, wsPath)
	if err != nil {
		ui.logger.Error("workspace ls failed", "path", wsPath, "error", err)
		ui.renderError(w, "Failed to list workspace", err)
		return
	}

	data := map[string]any{
		"Title":   "Workspace - GoWe",
		"Session": sess,
		"Path":    wsPath,
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
	if ui.adminChecker != nil {
		return ui.adminChecker.IsAdmin(username)
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

	if search := r.URL.Query().Get("search"); search != "" {
		opts.Search = strings.TrimSpace(search)
	}

	if sortBy := r.URL.Query().Get("sort"); sortBy != "" {
		opts.SortBy = sortBy
	}

	if sortDir := r.URL.Query().Get("dir"); sortDir != "" {
		opts.SortDir = strings.ToLower(sortDir)
	}

	return opts
}

// filterQuery builds a URL query suffix (&key=val&...) from non-empty key-value pairs.
// Empty values are skipped. Keys are sorted lexicographically by url.Values.Encode().
// Returns template.URL so html/template won't double-encode in href attributes.
func filterQuery(pairs ...string) template.URL {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			v.Set(pairs[i], pairs[i+1])
		}
	}
	encoded := v.Encode()
	if encoded == "" {
		return ""
	}
	return template.URL("&" + encoded)
}

func (ui *UI) buildPagination(opts model.ListOptions, total, itemCount int, filterParams template.URL, pageSizes []int) map[string]any {
	hasMore := opts.Offset+opts.Limit < total
	hasPrev := opts.Offset > 0

	// Last page offset: largest multiple of limit that is < total.
	lastOffset := 0
	if total > opts.Limit {
		lastOffset = ((total - 1) / opts.Limit) * opts.Limit
	}

	totalPages := 1
	if total > 0 {
		totalPages = ((total - 1) / max(opts.Limit, 1)) + 1
	}

	return map[string]any{
		"Total":        total,
		"Limit":        opts.Limit,
		"Offset":       opts.Offset,
		"HasMore":      hasMore,
		"HasPrev":      hasPrev,
		"NextOffset":   opts.Offset + opts.Limit,
		"PrevOffset":   max(0, opts.Offset-opts.Limit),
		"LastOffset":   lastOffset,
		"Page":         (opts.Offset / opts.Limit) + 1,
		"TotalPages":   totalPages,
		"FilterParams": filterParams,
		"ItemCount":    itemCount,
		"PageSizes":    pageSizes,
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
