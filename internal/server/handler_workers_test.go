package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// runSecondsSampleCount sums gowe_task_run_seconds samples across every
// label combination, via the public Gatherer — tests here only care about
// exactly-once counting, not which labels won.
func runSecondsSampleCount(t *testing.T, reg *metrics.Registry) uint64 {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gowe_task_run_seconds" {
			continue
		}
		var total uint64
		for _, m := range mf.GetMetric() {
			total += m.GetHistogram().GetSampleCount()
		}
		return total
	}
	return 0
}

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

// seedQueuedWorkerTask creates a QUEUED worker-executor task directly in the
// store (emulating scheduler dispatch, which sets external_id to the task's
// own id via executor.Submit) so it can be checked out through the API.
func seedQueuedWorkerTask(t *testing.T, srv *Server, subID, id string) string {
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
	}
	if err := srv.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed queued worker task: %v", err)
	}
	return id
}

// checkoutTask checks out the next task for workerID through the API.
func checkoutTask(t *testing.T, srv *Server, workerID string) *model.Task {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/workers/"+workerID+"/work", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("checkout: status=%d, body=%s", w.Code, w.Body.String())
	}
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	var task model.Task
	if err := json.Unmarshal(env.Data, &task); err != nil {
		t.Fatalf("decode checkout task: %v", err)
	}
	return &task
}

func TestWorkerTaskComplete(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_wc_1")

	// The worker must own the task (checkout sets external_id) before its
	// report is accepted.
	checked := checkoutTask(t, srv, workerID)
	if checked.ID != taskID {
		t.Fatalf("checked out %s, want %s", checked.ID, taskID)
	}

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

// TestWorkerTaskComplete_AlreadyTerminal409: a duplicate report on a task that
// already reached a terminal state is deliberately refused. Extended to
// verify exactly-once metrics observation: checkoutTask stamps started_at
// (seedQueuedWorkerTask alone does not), so the first report observes one
// gowe_task_run_seconds sample and the refused duplicate observes none.
func TestWorkerTaskComplete_AlreadyTerminal409(t *testing.T) {
	reg := metrics.NewRegistry(metrics.Config{})
	srv := testServer(WithMetrics(reg))
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_wc_dup")
	checkoutTask(t, srv, workerID)

	body := `{"state":"SUCCESS","exit_code":0}`
	if w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", body); w.Code != http.StatusOK {
		t.Fatalf("first report: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate report: status=%d, want 409, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrConflict {
		t.Errorf("error = %+v, want CONFLICT", env.Error)
	}
	if n := runSecondsSampleCount(t, reg); n != 1 {
		t.Errorf("gowe_task_run_seconds samples = %d, want 1 (exactly-once despite the duplicate report)", n)
	}
}

// racyTerminalizeStore wraps a real store.Store and, on TerminalizeTask,
// races the caller: it first flips the target row to FAILED via the
// embedded store (a write that itself wins, since the row is not yet
// terminal), then delegates the caller's original TerminalizeTask call —
// which now loses its own CAS because the row is already terminal. This
// models a task that reached a terminal state (e.g. a cancel fan-out
// SKIPPED it, or another report raced this one) strictly between the
// handler's earlier GetTask/IsTerminal pre-check and its TerminalizeTask
// call, without needing real goroutines to hit the window.
type racyTerminalizeStore struct {
	store.Store
}

func (r *racyTerminalizeStore) TerminalizeTask(ctx context.Context, task *model.Task) (bool, error) {
	racer := *task
	racer.State = model.TaskStateFailed
	now := time.Now().UTC()
	racer.CompletedAt = &now
	if _, err := r.Store.TerminalizeTask(ctx, &racer); err != nil {
		return false, err
	}
	return r.Store.TerminalizeTask(ctx, task)
}

// TestWorkerTaskComplete_CASLoss_AppliedGate409NoObservation covers the
// applied-gate on the complete handler's CAS write (internal/server/handler_workers.go
// TerminalizeTask call around line 403): the pre-check task.State.IsTerminal()
// passes (the task is RUNNING when read), but the CAS itself returns
// applied=false because the row was terminalized concurrently. The handler
// must respond 409 and must NOT observe a gowe_task_run_seconds sample for
// the losing write — mutation-tested by temporarily moving the observation
// into the !applied branch (see task notes); reverted after confirming that
// mutation makes this test fail.
func TestWorkerTaskComplete_CASLoss_AppliedGate409NoObservation(t *testing.T) {
	reg := metrics.NewRegistry(metrics.Config{})
	srv, realStore := testServerWithStore(WithMetrics(reg))
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_wc_cas_loss")
	checkoutTask(t, srv, workerID)

	// Swap in the racing store only for the complete call: the pre-check
	// GetTask still reads a RUNNING row through it, but TerminalizeTask
	// loses its own CAS to the race it injects.
	srv.store = &racyTerminalizeStore{Store: realStore}

	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete",
		`{"state":"SUCCESS","exit_code":0}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrConflict {
		t.Errorf("error = %+v, want CONFLICT", env.Error)
	}
	if n := runSecondsSampleCount(t, reg); n != 0 {
		t.Errorf("gowe_task_run_seconds samples = %d, want 0 (CAS loss must not observe)", n)
	}

	got, err := realStore.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateFailed {
		t.Errorf("task state = %s, want FAILED (the race's own write won)", got.State)
	}
}

// TestWorkerTaskComplete_NotOwner409: a report from a worker that does not own
// the task (external_id mismatch) is refused, and the task is untouched.
func TestWorkerTaskComplete_NotOwner409(t *testing.T) {
	srv := testServer()
	ownerID := registerTestWorker(t, srv)
	otherID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_wc_owner")
	checkoutTask(t, srv, ownerID)

	w, _ := doPut(t, srv, "/api/v1/workers/"+otherID+"/tasks/"+taskID+"/complete",
		`{"state":"FAILED","exit_code":1}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
	}

	got, err := srv.store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != model.TaskStateRunning || got.ExternalID != ownerID {
		t.Errorf("task = %s/%s, want RUNNING/%s (untouched by non-owner report)", got.State, got.ExternalID, ownerID)
	}
}

// TestWorkerTaskComplete_RequeuedOwnership: after a requeue (external_id
// cleared) the former owner's late report is refused; the worker that
// re-checks the task out is accepted (F-K).
func TestWorkerTaskComplete_RequeuedOwnership(t *testing.T) {
	srv := testServer()
	oldWorker := registerTestWorker(t, srv)
	newWorker := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_wc_requeue")
	checkoutTask(t, srv, oldWorker)

	// Server-side requeue (e.g. the stale-worker reaper): external_id = ''.
	if _, err := srv.store.RequeueWorkerTasks(context.Background(), oldWorker); err != nil {
		t.Fatalf("requeue: %v", err)
	}

	// The former owner's late report must be refused.
	w, _ := doPut(t, srv, "/api/v1/workers/"+oldWorker+"/tasks/"+taskID+"/complete",
		`{"state":"SUCCESS","exit_code":0}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale owner report: status=%d, want 409, body=%s", w.Code, w.Body.String())
	}

	// The new owner checks it out and reports successfully.
	checked := checkoutTask(t, srv, newWorker)
	if checked.ID != taskID {
		t.Fatalf("re-checkout got %s, want %s", checked.ID, taskID)
	}
	w, _ = doPut(t, srv, "/api/v1/workers/"+newWorker+"/tasks/"+taskID+"/complete",
		`{"state":"SUCCESS","exit_code":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("new owner report: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	// And the former owner is still refused (terminal now).
	w, _ = doPut(t, srv, "/api/v1/workers/"+oldWorker+"/tasks/"+taskID+"/complete",
		`{"state":"FAILED","exit_code":1}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale owner after terminal: status=%d, want 409, body=%s", w.Code, w.Body.String())
	}
}

// TestWorkerTaskStatus_Guards: the status endpoint accepts only non-terminal
// transitions on tasks the reporting worker owns (F-L + F-K).
func TestWorkerTaskStatus_Guards(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	ctx := context.Background()

	t.Run("terminal current state refused", func(t *testing.T) {
		taskID := seedQueuedWorkerTask(t, srv, subID, "task_st_term")
		task, _ := srv.store.GetTask(ctx, taskID)
		task.State = model.TaskStateSuccess
		if err := srv.store.UpdateTask(ctx, task); err != nil {
			t.Fatalf("terminalize seed: %v", err)
		}
		w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/status", `{"state":"RUNNING"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("terminal target refused", func(t *testing.T) {
		taskID := seedQueuedWorkerTask(t, srv, subID, "task_st_target")
		checkoutTask(t, srv, workerID) // RUNNING, owned by workerID
		w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/status", `{"state":"SUCCESS"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
		}
		got, _ := srv.store.GetTask(ctx, taskID)
		if got.State != model.TaskStateRunning {
			t.Errorf("state = %s, want RUNNING (terminalizing via /status must be refused)", got.State)
		}
	})

	t.Run("non-owner refused", func(t *testing.T) {
		taskID := seedQueuedWorkerTask(t, srv, subID, "task_st_owner")
		// Not checked out: external_id is the task's own id, not the worker's.
		w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/status", `{"state":"RUNNING"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
		}
		got, _ := srv.store.GetTask(ctx, taskID)
		if got.State != model.TaskStateQueued {
			t.Errorf("state = %s, want QUEUED (untouched)", got.State)
		}
	})

	t.Run("invalid transition refused", func(t *testing.T) {
		taskID := seedQueuedWorkerTask(t, srv, subID, "task_st_trans")
		task, _ := srv.store.GetTask(ctx, taskID)
		task.ExternalID = workerID // owner, still QUEUED
		if err := srv.store.UpdateTask(ctx, task); err != nil {
			t.Fatalf("set owner: %v", err)
		}
		w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/status", `{"state":"PENDING"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("owner non-terminal transition accepted", func(t *testing.T) {
		taskID := seedQueuedWorkerTask(t, srv, subID, "task_st_ok")
		task, _ := srv.store.GetTask(ctx, taskID)
		task.ExternalID = workerID // owner, still QUEUED
		if err := srv.store.UpdateTask(ctx, task); err != nil {
			t.Fatalf("set owner: %v", err)
		}
		w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/status", `{"state":"RUNNING"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
		}
		got, _ := srv.store.GetTask(ctx, taskID)
		if got.State != model.TaskStateRunning {
			t.Errorf("state = %s, want RUNNING", got.State)
		}
		if got.ExternalID != workerID {
			t.Errorf("external_id = %q, want %q (CAS write must not touch it)", got.ExternalID, workerID)
		}
	})
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
