package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/me/gowe/internal/worker"
	"github.com/me/gowe/pkg/model"
)

// seedQueuedWorkerTaskWithMaxRetries is seedQueuedWorkerTask (handler_workers_test.go)
// plus an explicit MaxRetries, so a "permanent" completion's effect (dropping
// MaxRetries to RetryCount) is observable against a value that would
// otherwise clearly permit further retries.
func seedQueuedWorkerTaskWithMaxRetries(t *testing.T, srv *Server, subID, id string, maxRetries int) string {
	t.Helper()
	task := &model.Task{
		ID:           id,
		SubmissionID: subID,
		StepID:       "step1",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		ExternalID:   id,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
		MaxRetries:   maxRetries,
	}
	if err := srv.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed queued worker task: %v", err)
	}
	return id
}

// TestWorkerTaskComplete_Permanent covers #273 item 6's worker-side
// non-retryable path: a FAILED completion with "permanent": true must apply
// the existing MaxRetries=RetryCount idiom (making the task terminal),
// while a FAILED completion that omits the field (old worker, or any
// ordinary failure) must leave MaxRetries untouched so normal retry
// behavior is preserved.
func TestWorkerTaskComplete_Permanent(t *testing.T) {
	tests := []struct {
		name           string
		id             string
		body           string
		wantMaxRetries int
	}{
		{
			name:           `permanent:true drops MaxRetries to RetryCount`,
			id:             "task_iv_perm_true",
			body:           `{"state":"FAILED","exit_code":1,"stderr":"bad input","permanent":true}`,
			wantMaxRetries: 0, // RetryCount starts at 0
		},
		{
			name:           `permanent absent leaves MaxRetries untouched (normal retries)`,
			id:             "task_iv_perm_absent",
			body:           `{"state":"FAILED","exit_code":1,"stderr":"transient"}`,
			wantMaxRetries: 3,
		},
		{
			name:           `permanent:false explicit leaves MaxRetries untouched`,
			id:             "task_iv_perm_false",
			body:           `{"state":"FAILED","exit_code":1,"stderr":"transient","permanent":false}`,
			wantMaxRetries: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testServer()
			workerID := registerTestWorker(t, srv)
			_, subID := createTestSubmission(t, srv)
			taskID := seedQueuedWorkerTaskWithMaxRetries(t, srv, subID, tt.id, 3)
			checkoutTask(t, srv, workerID)

			w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", tt.body)
			if w.Code != http.StatusOK {
				t.Fatalf("complete: status=%d, body=%s", w.Code, w.Body.String())
			}
			_ = env

			task, err := srv.store.GetTask(context.Background(), taskID)
			if err != nil || task == nil {
				t.Fatalf("get task: %v", err)
			}
			if task.State != model.TaskStateFailed {
				t.Fatalf("task.State = %q, want FAILED", task.State)
			}
			if task.MaxRetries != tt.wantMaxRetries {
				t.Errorf("task.MaxRetries = %d, want %d", task.MaxRetries, tt.wantMaxRetries)
			}
		})
	}
}

// TestWorkerClient_TaskResult_PermanentFieldRoundTrips pins the wire
// contract between internal/worker.TaskResult (the worker's completion
// payload, which I own) and this package's handleWorkerTaskComplete decode
// struct (which I also own): Permanent is "omitempty", so an old worker's
// zero-value payload (or any ordinary failure) serializes with no
// "permanent" key at all, decoding to the server's zero-value default
// (false, i.e. normal retries).
func TestWorkerClient_TaskResult_PermanentFieldRoundTrips(t *testing.T) {
	serverDecode := func(t *testing.T, b []byte) bool {
		t.Helper()
		var decoded struct {
			Permanent bool `json:"permanent"`
		}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return decoded.Permanent
	}

	permanent, err := json.Marshal(worker.TaskResult{State: model.TaskStateFailed, Permanent: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !serverDecode(t, permanent) {
		t.Error("permanent:true did not round-trip to the server's decode struct")
	}

	ordinary, err := json.Marshal(worker.TaskResult{State: model.TaskStateFailed})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if serverDecode(t, ordinary) {
		t.Error("an ordinary (Permanent: false) TaskResult must omit \"permanent\" and decode to false")
	}
}
