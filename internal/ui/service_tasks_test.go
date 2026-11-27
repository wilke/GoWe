package ui

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// A service task (gowe:Service) is never re-run from the web UI, and a
// worker holding a running one is not deleted.

// seedFailedServiceTask creates a FAILED worker service task on subID.
func seedFailedServiceTask(t *testing.T, st *store.SQLiteStore, taskID, subID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.CreateTask(context.Background(), &model.Task{
		ID: taskID, SubmissionID: subID, StepID: "step1", State: model.TaskStateFailed,
		ExecutorType: model.ExecutorTypeWorker, Inputs: map[string]any{}, Outputs: map[string]any{},
		Job: map[string]any{}, ScatterIndex: -1, Stderr: "service exited", StartedAt: &now, CompletedAt: &now,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}},
	}); err != nil {
		t.Fatalf("create task %s: %v", taskID, err)
	}
}

// Resume, RecomputeFailed and TaskRecompute skip a FAILED service task and
// report it like a group-denied task; the other failed tasks are requeued.
func TestRequeueHandlers_ServiceTaskNotRerun(t *testing.T) {
	const reason = model.ServiceNotRerun
	owner := &model.Session{ID: "s1", Username: "alice", Role: "user"}

	t.Run("task-recompute refused", func(t *testing.T) {
		st := setupTestStore(t)
		defer st.Close()
		sub := seedRecomputeTestSubmission(t, st, "sub_svc_tr", model.SubmissionStateFailed, "alice")
		seedFailedServiceTask(t, st, "task_svc", sub.ID)

		w := callRequeue(t, newTestUI(st), "task-recompute", sub.ID, "task_svc", owner)
		if w.Code != http.StatusForbidden || w.Body.String() != reason {
			t.Fatalf("response = %d %q, want 403 %q", w.Code, w.Body.String(), reason)
		}
		if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateFailed {
			t.Errorf("task state = %s, want unchanged FAILED", got)
		}
		if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateFailed {
			t.Errorf("submission state = %s, want unchanged FAILED", got)
		}
	})

	for _, h := range []struct{ handler, verb string }{{"resume", "requeued"}, {"recompute-failed", "recomputed"}} {
		t.Run(h.handler+": service skipped, other task requeued", func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			sub := seedRecomputeTestSubmission(t, st, "sub_svc_bulk", model.SubmissionStateFailed, "alice")
			seedFailedServiceTask(t, st, "task_svc", sub.ID)
			seedFailedTaskIn(t, st, "task_plain", sub.ID, model.ExecutorTypeWorker, "llm")

			w := callRequeue(t, newTestUI(st), h.handler, sub.ID, "", owner)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
			}
			if got, want := w.Body.String(), h.verb+" 1, skipped 0, denied 1: "+reason; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateFailed {
				t.Errorf("service task state = %s, want unchanged FAILED", got)
			}
			if got := taskStateOf(t, st, "task_plain"); got != model.TaskStateQueued {
				t.Errorf("ordinary task state = %s, want QUEUED", got)
			}
		})

		t.Run(h.handler+": only the service failed, nothing changes", func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			sub := seedRecomputeTestSubmission(t, st, "sub_svc_only", model.SubmissionStateFailed, "alice")
			seedFailedServiceTask(t, st, "task_svc", sub.ID)

			w := callRequeue(t, newTestUI(st), h.handler, sub.ID, "", owner)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
			}
			if got, want := w.Body.String(), h.verb+" 0, skipped 0, denied 1: "+reason; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateFailed {
				t.Errorf("task state = %s, want unchanged FAILED", got)
			}
			if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateFailed {
				t.Errorf("submission state = %s, want unchanged FAILED", got)
			}
		})
	}
}

// seedServiceWorker stores an offline worker silent for an hour that holds
// a RUNNING service task.
func seedServiceWorker(t *testing.T, st *store.SQLiteStore, workerID, taskID string) {
	t.Helper()
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	if err := st.CreateWorker(ctx, &model.Worker{
		ID: workerID, Name: workerID, Hostname: "h", Group: "llm", State: model.WorkerStateOffline,
		Runtime: model.RuntimeNone, LastSeen: old, RegisteredAt: old, ServiceMode: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateTask(ctx, &model.Task{
		ID: taskID, SubmissionID: "sub_svc", StepID: "s", State: model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker, ExternalID: workerID, StartedAt: &old,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// The UI delete answers 409 for a worker holding a running service.
func TestHandleWorkerDelete_RunningService(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	seedServiceWorker(t, st, "wrk_svc", "task_svc")

	req := httptest.NewRequest(http.MethodDelete, "/workers/wrk_svc", nil)
	req.SetPathValue("id", "wrk_svc")
	rec := httptest.NewRecorder()
	newTestUI(st).HandleWorkerDelete(rec, req)

	const want = "worker still holds 1 running service(s); cancel their submissions first"
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("response = %d %q, want 409 containing %q", rec.Code, rec.Body.String(), want)
	}
	if wk, err := st.GetWorker(context.Background(), "wrk_svc"); err != nil || wk == nil {
		t.Errorf("worker = (%v, %v), want kept", wk, err)
	}
	if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateRunning {
		t.Errorf("service task = %s, want RUNNING", got)
	}
}

// The purge of offline workers keeps a worker holding a running service and
// goes on with the others.
func TestHandleWorkerPurgeOffline_KeepsServiceWorkers(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()
	seedServiceWorker(t, st, "wrk_svc", "task_svc")
	old := time.Now().UTC().Add(-time.Hour)
	if err := st.CreateWorker(ctx, &model.Worker{
		ID: "wrk_old", Name: "wrk_old", Hostname: "h", State: model.WorkerStateOffline,
		Runtime: model.RuntimeNone, LastSeen: old, RegisteredAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	u := New(st, slog.New(slog.NewTextHandler(&logs, nil)), Config{})

	rec := httptest.NewRecorder()
	u.HandleWorkerPurgeOffline(rec, httptest.NewRequest(http.MethodPost, "/workers/purge-offline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if wk, _ := st.GetWorker(ctx, "wrk_svc"); wk == nil {
		t.Error("wrk_svc was removed while holding a running service")
	}
	if wk, _ := st.GetWorker(ctx, "wrk_old"); wk != nil {
		t.Error("wrk_old was not removed")
	}
	if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateRunning {
		t.Errorf("service task = %s, want RUNNING", got)
	}
	out := logs.String()
	for _, want := range []string{"removed=1", "skipped_running_services=1", "failed=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "level=WARN") {
		t.Errorf("a kept service worker is not a failure (no WARN):\n%s", out)
	}
}
