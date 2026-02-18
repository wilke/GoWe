package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func doPut(t *testing.T, srv *Server, path, body string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func doDelete(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// registerTestWorker registers a worker and returns its ID.
func registerTestWorker(t *testing.T, srv *Server) string {
	t.Helper()
	body := `{"name":"test-worker","hostname":"localhost","runtime":"none"}`
	w, env := doPost(t, srv, "/api/v1/workers/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("register worker: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	id, ok := data["id"].(string)
	if !ok || !strings.HasPrefix(id, "wrk_") {
		t.Fatalf("worker id = %q, want wrk_ prefix", id)
	}
	return id
}

func TestRegisterWorker(t *testing.T) {
	srv := testServer()
	body := `{"name":"my-worker","hostname":"host1","runtime":"docker","labels":{"env":"prod"}}`
	w, env := doPost(t, srv, "/api/v1/workers/", body)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if !strings.HasPrefix(data["id"].(string), "wrk_") {
		t.Errorf("id = %v, want wrk_ prefix", data["id"])
	}
	if data["name"] != "my-worker" {
		t.Errorf("name = %v, want my-worker", data["name"])
	}
	if data["runtime"] != "docker" {
		t.Errorf("runtime = %v, want docker", data["runtime"])
	}
	if data["state"] != "online" {
		t.Errorf("state = %v, want online", data["state"])
	}
}

func TestRegisterWorker_MissingName(t *testing.T) {
	srv := testServer()
	w, env := doPost(t, srv, "/api/v1/workers/", `{"hostname":"host"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Errorf("error = %v, want VALIDATION_ERROR", env.Error)
	}
}

func TestWorkerHeartbeat(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["worker_id"] != workerID {
		t.Errorf("worker_id = %v, want %s", data["worker_id"], workerID)
	}
}

func TestWorkerHeartbeat_NotFound(t *testing.T) {
	srv := testServer()
	w, _ := doPut(t, srv, "/api/v1/workers/wrk_nonexistent/heartbeat", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestWorkerCheckout_NoWork(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	req := httptest.NewRequest("GET", "/api/v1/workers/"+workerID+"/work", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status=%d, want 204 (no work), body=%s", w.Code, w.Body.String())
	}
}

func TestWorkerCheckout_NotFound(t *testing.T) {
	srv := testServer()
	req := httptest.NewRequest("GET", "/api/v1/workers/wrk_nonexistent/work", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestWorkerTaskComplete(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	// Create a workflow + submission to get a task.
	_, subID := createTestSubmission(t, srv)

	// Get the task IDs.
	env := doGet(t, srv, "/api/v1/submissions/"+subID+"/tasks/")
	var tasks []map[string]any
	json.Unmarshal(env.Data, &tasks)
	if len(tasks) == 0 {
		t.Fatal("no tasks found")
	}
	taskID := tasks[0]["id"].(string)

	// Report completion.
	body := `{"state":"SUCCESS","exit_code":0,"stdout":"output","stderr":"","outputs":{"result":"file:///tmp/out"}}`
	w, env2 := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env2.Data, &data)
	if data["task_id"] != taskID {
		t.Errorf("task_id = %v, want %s", data["task_id"], taskID)
	}
	if data["state"] != "SUCCESS" {
		t.Errorf("state = %v, want SUCCESS", data["state"])
	}
}

func TestWorkerTaskComplete_NotFound(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/task_nonexistent/complete",
		`{"state":"SUCCESS","exit_code":0}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestDeregisterWorker(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	w := doDelete(t, srv, "/api/v1/workers/"+workerID)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	// Should be gone now.
	req := httptest.NewRequest("GET", "/api/v1/workers/"+workerID+"/work", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("after delete: status=%d, want 404", w2.Code)
	}
}

func TestDeregisterWorker_NotFound(t *testing.T) {
	srv := testServer()
	w := doDelete(t, srv, "/api/v1/workers/wrk_nonexistent")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", w.Code)
	}
}

func TestListWorkers(t *testing.T) {
	srv := testServer()

	// Empty list.
	env := doGet(t, srv, "/api/v1/workers/")
	var workers []any
	json.Unmarshal(env.Data, &workers)
	if len(workers) != 0 {
		t.Errorf("expected empty list, got %d workers", len(workers))
	}

	// Register a worker, then list.
	registerTestWorker(t, srv)
	env = doGet(t, srv, "/api/v1/workers/")
	json.Unmarshal(env.Data, &workers)
	if len(workers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(workers))
	}
}
