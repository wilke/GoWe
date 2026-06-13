package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// seedRunningWorkerTask attaches a RUNNING worker task to an existing submission,
// attributed to workerID and started startedAgo in the past.
func seedRunningWorkerTask(t *testing.T, srv *Server, subID, taskID, workerID string, startedAgo time.Duration) {
	t.Helper()
	started := time.Now().UTC().Add(-startedAgo)
	task := &model.Task{
		ID:           taskID,
		SubmissionID: subID,
		StepID:       "assemble",
		State:        model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker,
		ExternalID:   workerID,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
		StartedAt:    &started,
	}
	if err := srv.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

// heartbeatResp is the decoded data field of a heartbeat response.
type heartbeatResp struct {
	WorkerID    string   `json:"worker_id"`
	State       string   `json:"state"`
	CancelTasks []string `json:"cancel_tasks"`
}

// A worker that heartbeats without reporting a task the DB still attributes to it
// (server-restart orphan) gets that task requeued (#118).
func TestWorkerHeartbeat_ReconcilesOrphan(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	seedRunningWorkerTask(t, srv, subID, "task_orphan", workerID, 10*time.Minute)

	// Heartbeat reporting an empty running set.
	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", `{"running_tasks":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	got, _ := srv.store.GetTask(context.Background(), "task_orphan")
	if got.State != model.TaskStateQueued {
		t.Errorf("orphan task state = %s, want QUEUED (requeued)", got.State)
	}
	if got.ExternalID != "" {
		t.Errorf("orphan external_id = %q, want cleared", got.ExternalID)
	}
}

// A worker that reports the task as still running keeps it RUNNING.
func TestWorkerHeartbeat_KeepsReportedTask(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	seedRunningWorkerTask(t, srv, subID, "task_inflight", workerID, 10*time.Minute)

	body := `{"running_tasks":["task_inflight"]}`
	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", w.Code)
	}

	got, _ := srv.store.GetTask(context.Background(), "task_inflight")
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %s, want RUNNING (worker still reports it)", got.State)
	}
}

// When the owning submission is cancelled, the heartbeat tells the worker to kill
// the task it is running (#113).
func TestWorkerHeartbeat_ReturnsCancelTasks(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	seedRunningWorkerTask(t, srv, subID, "task_running", workerID, time.Minute)

	// Cancel the submission via the API.
	req := httptest.NewRequest("PUT", "/api/v1/submissions/"+subID+"/cancel", nil)
	cw := httptest.NewRecorder()
	srv.ServeHTTP(cw, req)
	if cw.Code != http.StatusOK {
		t.Fatalf("cancel: status=%d, body=%s", cw.Code, cw.Body.String())
	}

	// Worker heartbeats, reporting the task as still running.
	_, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", `{"running_tasks":["task_running"]}`)
	var data heartbeatResp
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode heartbeat data: %v", err)
	}
	if len(data.CancelTasks) != 1 || data.CancelTasks[0] != "task_running" {
		t.Fatalf("cancel_tasks = %v, want [task_running]", data.CancelTasks)
	}
}

// An empty heartbeat body must still succeed (backward compatibility).
func TestWorkerHeartbeat_EmptyBodyStillWorks(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var data heartbeatResp
	json.Unmarshal(env.Data, &data)
	if data.WorkerID != workerID {
		t.Errorf("worker_id = %q, want %q", data.WorkerID, workerID)
	}
}
