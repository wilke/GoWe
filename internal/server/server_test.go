package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func testServer(opts ...Option) *Server {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		panic("open test store: " + err.Error())
	}
	if err := st.Migrate(context.Background()); err != nil {
		panic("migrate test store: " + err.Error())
	}
	return New(config.DefaultServerConfig(), st, nil, logger, opts...)
}

// testApps returns a static app list for tests that exercise the /apps endpoints.
var testAppList = []map[string]any{
	{
		"id":          "GenomeAssembly2",
		"label":       "Genome Assembly",
		"description": "Assemble reads into contigs using SPAdes, MEGAHIT, or other assemblers",
	},
	{
		"id":          "GenomeAnnotation",
		"label":       "Genome Annotation",
		"description": "Annotate a genome using RASTtk",
	},
	{
		"id":          "ComprehensiveGenomeAnalysis",
		"label":       "Comprehensive Genome Analysis",
		"description": "Assembly + Annotation + Analysis pipeline",
	},
}

// envelope is used to decode the standard response envelope.
type envelope struct {
	Status     string           `json:"status"`
	RequestID  string           `json:"request_id"`
	Timestamp  string           `json:"timestamp"`
	Data       json.RawMessage  `json:"data"`
	Pagination *model.Pagination `json:"pagination"`
	Error      *model.APIError  `json:"error"`
}

func doGet(t *testing.T, srv *Server, path string) envelope {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d, want 200, body=%s", path, w.Code, w.Body.String())
	}
	var env envelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("GET %s: invalid JSON: %v", path, err)
	}
	return env
}

func doPost(t *testing.T, srv *Server, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func loadPackedCWL(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "packed", "pipeline-packed.cwl"))
	if err != nil {
		t.Fatalf("load packed CWL: %v", err)
	}
	return string(data)
}

// createTestWorkflow POSTs a valid packed CWL workflow and returns its ID.
func createTestWorkflow(t *testing.T, srv *Server) string {
	t.Helper()
	cwlStr := loadPackedCWL(t)
	bodyJSON, _ := json.Marshal(map[string]string{
		"name": "test-workflow",
		"cwl":  cwlStr,
	})
	w, env := doPost(t, srv, "/api/v1/workflows/", string(bodyJSON))
	if w.Code != http.StatusCreated {
		t.Fatalf("create workflow: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	id, ok := data["id"].(string)
	if !ok {
		t.Fatalf("created workflow missing id, data=%v", data)
	}
	return id
}

// createTestSubmission creates a workflow and a submission, returning (workflowID, submissionID).
func createTestSubmission(t *testing.T, srv *Server) (string, string) {
	t.Helper()
	wfID := createTestWorkflow(t, srv)
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"labels":      map[string]string{"project": "test"},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID, ok := data["id"].(string)
	if !ok {
		t.Fatalf("created submission missing id, data=%v", data)
	}
	return wfID, subID
}

// --- Discovery & Health (unchanged) ---

func TestDiscovery(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/")
	if env.Status != "ok" {
		t.Errorf("status = %q, want ok", env.Status)
	}
	if env.RequestID == "" {
		t.Error("request_id is empty")
	}

	var data struct {
		Name      string `json:"name"`
		Endpoints []struct {
			Path string `json:"path"`
		} `json:"endpoints"`
	}
	json.Unmarshal(env.Data, &data)
	if data.Name != "GoWe API" {
		t.Errorf("name = %q, want GoWe API", data.Name)
	}
	if len(data.Endpoints) < 10 {
		t.Errorf("endpoints count = %d, want >= 10", len(data.Endpoints))
	}
}

func TestHealth(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/health")

	var data struct {
		Status    string `json:"status"`
		Version   string `json:"version"`
		GoVersion string `json:"go_version"`
	}
	json.Unmarshal(env.Data, &data)
	if data.Status != "healthy" {
		t.Errorf("health status = %q, want healthy", data.Status)
	}
	if data.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", data.Version)
	}
}

// --- Workflow CRUD (real parsing + SQLite) ---

func TestCreateWorkflow(t *testing.T) {
	srv := testServer()
	cwlStr := loadPackedCWL(t)
	bodyJSON, _ := json.Marshal(map[string]string{
		"name": "test-workflow",
		"cwl":  cwlStr,
	})
	w, env := doPost(t, srv, "/api/v1/workflows/", string(bodyJSON))

	if w.Code != http.StatusCreated {
		t.Fatalf("POST /workflows: status=%d, want 201, body=%s", w.Code, w.Body.String())
	}
	if env.Status != "ok" {
		t.Errorf("status = %q, want ok", env.Status)
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	id, ok := data["id"].(string)
	if !ok || !strings.HasPrefix(id, "wf_") {
		t.Errorf("id = %q, want wf_ prefix", id)
	}
	if data["name"] != "test-workflow" {
		t.Errorf("name = %v, want test-workflow", data["name"])
	}
	if data["cwl_version"] != "v1.2" {
		t.Errorf("cwl_version = %v, want v1.2", data["cwl_version"])
	}

	// Verify parsed steps.
	steps, ok := data["steps"].([]any)
	if !ok || len(steps) != 2 {
		t.Errorf("steps count = %v, want 2", data["steps"])
	}
}

func TestCreateWorkflow_InvalidJSON(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("POST", "/api/v1/workflows/", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Status != "error" {
		t.Errorf("status = %q, want error", env.Status)
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Errorf("error code = %v, want VALIDATION_ERROR", env.Error)
	}
}

func TestCreateWorkflow_MissingCWL(t *testing.T) {
	srv := testServer()
	w, env := doPost(t, srv, "/api/v1/workflows/", `{"name":"test"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Errorf("error = %v, want VALIDATION_ERROR", env.Error)
	}
}

func TestCreateWorkflow_InvalidCWL(t *testing.T) {
	srv := testServer()
	bodyJSON, _ := json.Marshal(map[string]string{
		"name": "bad",
		"cwl":  "cwlVersion: v1.2\nclass: Workflow\n",
	})
	w, _ := doPost(t, srv, "/api/v1/workflows/", string(bodyJSON))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
}

func TestCreateWorkflow_ValidationError(t *testing.T) {
	srv := testServer()
	cwl := `cwlVersion: v1.2
$graph:
  - id: tool1
    class: CommandLineTool
    inputs:
      x: { type: File }
    outputs:
      out: { type: File }
  - id: main
    class: Workflow
    inputs:
      input1: File
    outputs:
      output1:
        type: File
        outputSource: step1/out
    steps:
      step1:
        run: "#tool1"
        in:
          x: nonexistent/output
        out: [out]
`
	bodyJSON, _ := json.Marshal(map[string]string{"name": "bad", "cwl": cwl})
	w, env := doPost(t, srv, "/api/v1/workflows/", string(bodyJSON))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Errorf("error = %v, want VALIDATION_ERROR", env.Error)
	}
}

func TestListWorkflows(t *testing.T) {
	srv := testServer()

	// Empty list.
	env := doGet(t, srv, "/api/v1/workflows/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total != 0 {
		t.Errorf("pagination total = %d, want 0 (empty)", env.Pagination.Total)
	}

	// Create one, then list.
	createTestWorkflow(t, srv)
	env = doGet(t, srv, "/api/v1/workflows/")
	if env.Pagination.Total != 1 {
		t.Errorf("pagination total = %d, want 1", env.Pagination.Total)
	}
}

func TestGetWorkflow(t *testing.T) {
	srv := testServer()
	id := createTestWorkflow(t, srv)

	env := doGet(t, srv, "/api/v1/workflows/"+id)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["id"] != id {
		t.Errorf("id = %v, want %s", data["id"], id)
	}
	if data["name"] != "test-workflow" {
		t.Errorf("name = %v, want test-workflow", data["name"])
	}
}

func TestGetWorkflow_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/workflows/wf_nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestDeleteWorkflow(t *testing.T) {
	srv := testServer()
	id := createTestWorkflow(t, srv)

	req := httptest.NewRequest("DELETE", "/api/v1/workflows/"+id, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d, want 200", w.Code)
	}

	// Verify it's gone.
	req = httptest.NewRequest("GET", "/api/v1/workflows/"+id, nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete: status=%d, want 404", w.Code)
	}
}

func TestDeleteWorkflow_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("DELETE", "/api/v1/workflows/wf_nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestValidateWorkflow(t *testing.T) {
	srv := testServer()
	id := createTestWorkflow(t, srv)

	req := httptest.NewRequest("POST", "/api/v1/workflows/"+id+"/validate", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["valid"] != true {
		t.Errorf("valid = %v, want true", data["valid"])
	}
}

func TestValidateWorkflow_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("POST", "/api/v1/workflows/wf_nonexistent/validate", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

// --- Submissions (real persistence) ---

func TestCreateSubmission(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"labels":      map[string]string{"project": "test"},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if id, ok := data["id"].(string); !ok || !strings.HasPrefix(id, "sub_") {
		t.Errorf("id = %q, want sub_ prefix", data["id"])
	}
	if data["state"] != "PENDING" {
		t.Errorf("state = %v, want PENDING", data["state"])
	}
	if data["workflow_id"] != wfID {
		t.Errorf("workflow_id = %v, want %s", data["workflow_id"], wfID)
	}
	// Should have 2 tasks (assemble + annotate).
	tasks, ok := data["tasks"].([]any)
	if !ok || len(tasks) != 2 {
		t.Errorf("tasks count = %v, want 2", data["tasks"])
	}
}

func TestCreateSubmission_WorkflowNotFound(t *testing.T) {
	srv := testServer()
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": "wf_nonexistent",
		"inputs":      map[string]any{},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrNotFound {
		t.Errorf("error = %v, want NOT_FOUND", env.Error)
	}
}

func TestCreateSubmission_DryRun(t *testing.T) {
	srv := testServer()
	body := `{"workflow_id":"wf_123","inputs":{}}`
	req := httptest.NewRequest("POST", "/api/v1/submissions/?dry_run=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", data["dry_run"])
	}
	if data["valid"] != true {
		t.Errorf("valid = %v, want true", data["valid"])
	}
}

func TestListSubmissions(t *testing.T) {
	srv := testServer()

	// Empty list.
	env := doGet(t, srv, "/api/v1/submissions/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total != 0 {
		t.Errorf("total = %d, want 0 (empty)", env.Pagination.Total)
	}

	// Create one, then list.
	createTestSubmission(t, srv)
	env = doGet(t, srv, "/api/v1/submissions/")
	if env.Pagination.Total != 1 {
		t.Errorf("total = %d, want 1", env.Pagination.Total)
	}
}

func TestGetSubmission(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	env := doGet(t, srv, "/api/v1/submissions/"+subID)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["id"] != subID {
		t.Errorf("id = %v, want %s", data["id"], subID)
	}
	if data["state"] != "PENDING" {
		t.Errorf("state = %v, want PENDING", data["state"])
	}
}

func TestGetSubmission_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/submissions/sub_nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestCancelSubmission(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	req := httptest.NewRequest("PUT", "/api/v1/submissions/"+subID+"/cancel", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["state"] != "CANCELLED" {
		t.Errorf("state = %v, want CANCELLED", data["state"])
	}
	// 2 pending tasks should be cancelled.
	tasksCancelled, _ := data["tasks_cancelled"].(float64)
	if tasksCancelled != 2 {
		t.Errorf("tasks_cancelled = %v, want 2", data["tasks_cancelled"])
	}
}

func TestCancelSubmission_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("PUT", "/api/v1/submissions/sub_nonexistent/cancel", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

// --- Tasks (real persistence) ---

func TestListTasks(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	env := doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total != 2 {
		t.Errorf("total = %d, want 2", env.Pagination.Total)
	}
}

func TestListTasks_Empty(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/submissions/sub_nonexistent/tasks/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total != 0 {
		t.Errorf("total = %d, want 0", env.Pagination.Total)
	}
}

func TestGetTask(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	// List tasks to get a task ID.
	env := doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/")
	var tasks []map[string]any
	json.Unmarshal(env.Data, &tasks)
	if len(tasks) == 0 {
		t.Fatal("no tasks returned")
	}
	taskID := tasks[0]["id"].(string)

	// Get individual task.
	env = doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/"+taskID)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["id"] != taskID {
		t.Errorf("id = %v, want %s", data["id"], taskID)
	}
	if data["state"] != "PENDING" {
		t.Errorf("state = %v, want PENDING", data["state"])
	}
}

func TestGetTask_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/submissions/sub_x/tasks/task_nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestGetTaskLogs(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	// List tasks to get a task ID.
	env := doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/")
	var tasks []map[string]any
	json.Unmarshal(env.Data, &tasks)
	taskID := tasks[0]["id"].(string)

	env = doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/"+taskID+"/logs")
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["task_id"] != taskID {
		t.Errorf("task_id = %v, want %s", data["task_id"], taskID)
	}
	// Stdout/stderr should be empty for a newly created task.
	if data["stdout"] != "" {
		t.Errorf("stdout = %q, want empty", data["stdout"])
	}
}

// --- Apps (unchanged) ---

func TestListApps(t *testing.T) {
	srv := testServer(WithTestApps(testAppList))
	env := doGet(t, srv, "/api/v1/apps/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total < 3 {
		t.Errorf("total = %d, want >= 3", env.Pagination.Total)
	}
}

func TestListApps_NoBVBRC(t *testing.T) {
	srv := testServer() // no caller, no test apps
	env := doGet(t, srv, "/api/v1/apps/")
	if env.Pagination == nil {
		t.Fatal("expected pagination")
	}
	if env.Pagination.Total != 0 {
		t.Errorf("total = %d, want 0 when BV-BRC not configured", env.Pagination.Total)
	}
}

func TestGetApp(t *testing.T) {
	srv := testServer(WithTestApps(testAppList))
	env := doGet(t, srv, "/api/v1/apps/GenomeAssembly2")

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["id"] != "GenomeAssembly2" {
		t.Errorf("id = %v, want GenomeAssembly2", data["id"])
	}
	if data["label"] == nil {
		t.Error("expected label for GenomeAssembly2")
	}
}

func TestGetApp_NotFound(t *testing.T) {
	srv := testServer(WithTestApps(testAppList))
	req := httptest.NewRequest("GET", "/api/v1/apps/UnknownApp", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Status != "error" {
		t.Errorf("status = %q, want error", env.Status)
	}
	if env.Error.Code != model.ErrNotFound {
		t.Errorf("error code = %v, want NOT_FOUND", env.Error.Code)
	}
}

func TestGetApp_NoBVBRC(t *testing.T) {
	srv := testServer() // no caller, no test apps
	req := httptest.NewRequest("GET", "/api/v1/apps/GenomeAssembly2", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503", w.Code)
	}
}

func TestGetAppCWLTool(t *testing.T) {
	srv := testServer(WithTestApps(testAppList))
	env := doGet(t, srv, "/api/v1/apps/GenomeAssembly2/cwl-tool")

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["app_id"] != "GenomeAssembly2" {
		t.Errorf("app_id = %v, want GenomeAssembly2", data["app_id"])
	}
	cwl, ok := data["cwl_tool"].(string)
	if !ok || !strings.Contains(cwl, "goweHint") {
		t.Errorf("cwl_tool should contain goweHint, got %v", data["cwl_tool"])
	}
}

func TestGetAppCWLTool_NotFound(t *testing.T) {
	srv := testServer(WithTestApps(testAppList))
	req := httptest.NewRequest("GET", "/api/v1/apps/UnknownApp/cwl-tool", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestListWorkspace(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/workspace?path=/user@bvbrc/home/reads/")

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["path"] != "/user@bvbrc/home/reads/" {
		t.Errorf("path = %v, want /user@bvbrc/home/reads/", data["path"])
	}
	objects, ok := data["objects"].([]any)
	if !ok || len(objects) < 2 {
		t.Errorf("expected >= 2 objects, got %v", data["objects"])
	}
}

// --- Response Envelope (unchanged) ---

func TestResponseEnvelope_HasRequestID(t *testing.T) {
	srv := testServer()
	env := doGet(t, srv, "/api/v1/health")
	if !strings.HasPrefix(env.RequestID, "req_") {
		t.Errorf("request_id = %q, want req_ prefix", env.RequestID)
	}
	if env.Timestamp == "" {
		t.Error("timestamp is empty")
	}
}

func TestResponseEnvelope_XRequestIDHeader(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	xReqID := w.Header().Get("X-Request-ID")
	if !strings.HasPrefix(xReqID, "req_") {
		t.Errorf("X-Request-ID header = %q, want req_ prefix", xReqID)
	}
}
