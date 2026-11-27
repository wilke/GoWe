package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Review fixes for "a service task is never started a second time", on the
// HTTP side: /complete accepts only terminal states (and never SUCCESS for a
// service), the heartbeat tells an absent running_tasks from an empty one
// and asks the worker to stop services the server has ended, a submission
// with a service task is never retried, and only a key naming the group
// registers a service-mode worker.

// seedOwnedTask stores a worker task of workerID in state (RUNNING: started
// an hour ago), a service of group g when service is set.
func seedOwnedTask(t *testing.T, st store.Store, id, workerID string, service bool, state model.TaskState) {
	t.Helper()
	started := time.Now().UTC().Add(-time.Hour)
	tk := &model.Task{
		ID: id, SubmissionID: "sub_review", StepID: "s", State: state,
		ExecutorType: model.ExecutorTypeWorker, ExternalID: workerID, StartedAt: &started,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1, MaxRetries: 3,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "g"},
	}
	if service {
		tk.RuntimeHints.Service = &model.ServiceSpec{PortInput: "port"}
		tk.MaxRetries = 0
	}
	if state.IsTerminal() {
		tk.CompletedAt = &started
	}
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
}

// completeTask reports body for task id of workerID.
func completeTask(t *testing.T, srv *Server, workerID, id, body string) (int, envelope) {
	t.Helper()
	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+id+"/complete", body)
	return w.Code, env
}

// mustTask reads task id.
func mustTask(t *testing.T, st store.Store, id string) *model.Task {
	t.Helper()
	tk, err := st.GetTask(context.Background(), id)
	if err != nil || tk == nil {
		t.Fatalf("GetTask(%s) = (%v, %v)", id, tk, err)
	}
	return tk
}

// R4: a state that is not terminal is refused (400) and the row unchanged.
func TestWorkerTaskComplete_NonTerminalStateRefused(t *testing.T) {
	for _, state := range []string{"QUEUED", "PENDING", "RUNNING", "SCHEDULED", "RETRYING", "garbage", "success"} {
		t.Run(state, func(t *testing.T) {
			srv, st := testServerWithStore()
			workerID := registerTestWorker(t, srv)
			seedOwnedTask(t, st, "task_c", workerID, false, model.TaskStateRunning)

			code, env := completeTask(t, srv, workerID, "task_c", `{"state":"`+state+`","exit_code":0}`)
			if code != http.StatusBadRequest || env.Error == nil || env.Error.Code != model.ErrValidation {
				t.Fatalf("complete = %d %+v, want 400 VALIDATION", code, env.Error)
			}
			if tk := mustTask(t, st, "task_c"); tk.State != model.TaskStateRunning || tk.ExternalID != workerID || tk.CompletedAt != nil {
				t.Errorf("task = %s on %q (completed %v), want unchanged RUNNING", tk.State, tk.ExternalID, tk.CompletedAt)
			}
		})
	}
}

// R4: every state the Go worker sends still works for an ordinary task, and
// an omitted state is decided from the exit code as before.
func TestWorkerTaskComplete_BatchStates(t *testing.T) {
	tests := []struct {
		name, body string
		want       model.TaskState
	}{
		{"omitted, exit 0", `{"exit_code":0}`, model.TaskStateSuccess},
		{"omitted, exit 1", `{"exit_code":1}`, model.TaskStateFailed},
		{"SUCCESS", `{"state":"SUCCESS","exit_code":0}`, model.TaskStateSuccess},
		{"FAILED", `{"state":"FAILED","exit_code":2}`, model.TaskStateFailed},
		{"SKIPPED (cancelled)", `{"state":"SKIPPED","exit_code":-1}`, model.TaskStateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			workerID := registerTestWorker(t, srv)
			seedOwnedTask(t, st, "task_c", workerID, false, model.TaskStateRunning)
			if code, env := completeTask(t, srv, workerID, "task_c", tt.body); code != http.StatusOK {
				t.Fatalf("complete = %d %+v, want 200", code, env.Error)
			}
			if tk := mustTask(t, st, "task_c"); tk.State != tt.want {
				t.Errorf("task = %s, want %s", tk.State, tt.want)
			}
		})
	}
}

// R4: a service task must report its state, and never SUCCESS; FAILED is
// accepted, SKIPPED without a cancel of the submission (here: no submission
// row) is stored as FAILED (second review, item 2).
func TestWorkerTaskComplete_ServiceRules(t *testing.T) {
	tests := []struct {
		name, body string
		wantCode   int
		wantMsg    string
		want       model.TaskState
	}{
		{"state omitted, exit 0", `{"exit_code":0}`, http.StatusBadRequest, "must report its state explicitly", model.TaskStateRunning},
		{"SUCCESS", `{"state":"SUCCESS","exit_code":0}`, http.StatusBadRequest, ServiceSuccessRefused, model.TaskStateRunning},
		{"FAILED", `{"state":"FAILED","exit_code":137}`, http.StatusOK, "", model.TaskStateFailed},
		{"SKIPPED, no cancel", `{"state":"SKIPPED","exit_code":-1}`, http.StatusOK, "", model.TaskStateFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			workerID := registerTestWorker(t, srv)
			seedOwnedTask(t, st, "task_svc", workerID, true, model.TaskStateRunning)
			code, env := completeTask(t, srv, workerID, "task_svc", tt.body)
			if code != tt.wantCode {
				t.Fatalf("complete = %d %+v, want %d", code, env.Error, tt.wantCode)
			}
			if tt.wantMsg != "" && (env.Error == nil || !strings.Contains(env.Error.Message, tt.wantMsg)) {
				t.Errorf("error = %+v, want a message containing %q", env.Error, tt.wantMsg)
			}
			if tk := mustTask(t, st, "task_svc"); tk.State != tt.want {
				t.Errorf("task = %s, want %s", tk.State, tt.want)
			}
		})
	}
}

// seedServiceIn stores a RUNNING service task of workerID in submission
// subID, started an hour ago.
func seedServiceIn(t *testing.T, st store.Store, subID, id, workerID string) {
	t.Helper()
	started := time.Now().UTC().Add(-time.Hour)
	tk := &model.Task{
		ID: id, SubmissionID: subID, StepID: "s", State: model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker, ExternalID: workerID, StartedAt: &started,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "g", Service: &model.ServiceSpec{PortInput: "port"}},
	}
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
}

// Second review, item 2: SKIPPED for a service whose submission is not
// cancelled is stored as FAILED, with a line appended to the stderr the
// worker sent.
func TestWorkerTaskComplete_ServiceSkippedWithoutCancel(t *testing.T) {
	tests := []struct {
		name, stderr, want string
	}{
		{"stderr sent", "model server exited", "model server exited\n" + ServiceStoppedWithoutCancel},
		{"stderr ending in a newline", "bye\n", "bye\n" + ServiceStoppedWithoutCancel},
		{"no stderr", "", ServiceStoppedWithoutCancel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			workerID := registerTestWorker(t, srv)
			_, subID := createTestSubmission(t, srv)
			seedServiceIn(t, st, subID, "task_svc", workerID)

			body, _ := json.Marshal(map[string]any{"state": "SKIPPED", "exit_code": -1, "stderr": tt.stderr})
			code, env := completeTask(t, srv, workerID, "task_svc", string(body))
			if code != http.StatusOK {
				t.Fatalf("complete = %d %+v, want 200", code, env.Error)
			}
			var data map[string]any
			if err := json.Unmarshal(env.Data, &data); err != nil || data["state"] != string(model.TaskStateFailed) {
				t.Errorf("response = %v (%v), want state FAILED", data, err)
			}
			if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateFailed || tk.Stderr != tt.want {
				t.Errorf("task = %s stderr %q, want FAILED %q", tk.State, tk.Stderr, tt.want)
			}
		})
	}
}

// Second review, item 2: SKIPPED for a service after a cancel. Through the
// cancel route the task is already SKIPPED (cancelseq), so the report is
// refused as for any terminal task (409) and the row is unchanged; when the
// submission is CANCELLED but the task not yet terminal, SKIPPED is stored
// as sent.
func TestWorkerTaskComplete_ServiceSkippedAfterCancel(t *testing.T) {
	const body = `{"state":"SKIPPED","exit_code":-1,"stderr":"stopped"}`
	t.Run("cancel route already skipped the task", func(t *testing.T) {
		srv, st := testServerWithStore()
		workerID := registerTestWorker(t, srv)
		_, subID := createTestSubmission(t, srv)
		seedServiceIn(t, st, subID, "task_svc", workerID)
		if w, _ := doPut(t, srv, "/api/v1/submissions/"+subID+"/cancel", ""); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
		code, env := completeTask(t, srv, workerID, "task_svc", body)
		if code != http.StatusConflict || env.Error == nil || env.Error.Code != model.ErrConflict ||
			!strings.Contains(env.Error.Message, "already terminal (SKIPPED)") {
			t.Fatalf("complete = %d %+v, want 409 already terminal (SKIPPED)", code, env.Error)
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateSkipped || tk.Stderr != "" {
			t.Errorf("task = %s stderr %q, want SKIPPED, stderr untouched", tk.State, tk.Stderr)
		}
	})
	t.Run("submission cancelled, task still running", func(t *testing.T) {
		srv, st := testServerWithStore()
		workerID := registerTestWorker(t, srv)
		now := time.Now().UTC()
		sub := &model.Submission{
			ID: "sub_cancelled", WorkflowID: "wf_x", WorkflowName: "x", State: model.SubmissionStateCancelled,
			SubmittedBy: "anonymous", Inputs: map[string]any{}, Outputs: map[string]any{},
			Labels: map[string]string{}, CreatedAt: now, CompletedAt: &now,
		}
		if err := st.CreateSubmission(context.Background(), sub); err != nil {
			t.Fatal(err)
		}
		seedServiceIn(t, st, sub.ID, "task_svc", workerID)
		if code, env := completeTask(t, srv, workerID, "task_svc", body); code != http.StatusOK {
			t.Fatalf("complete = %d %+v, want 200", code, env.Error)
		}
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateSkipped || tk.Stderr != "stopped" {
			t.Errorf("task = %s stderr %q, want SKIPPED %q", tk.State, tk.Stderr, "stopped")
		}
	})
}

// Second review, item 4: a service-mode registration needs a valid host id
// (400, nothing stored); an absent, empty or legacy-style one does not
// count. An ordinary worker still registers without one.
func TestRegisterWorker_ServiceModeNeedsHostID(t *testing.T) {
	tests := []struct {
		name        string
		hostID      any // nil: field absent
		serviceMode bool
		want        int
	}{
		{"service mode, host id absent", nil, true, http.StatusBadRequest},
		{"service mode, host id empty", "", true, http.StatusBadRequest},
		{"service mode, legacy-style host id", model.LegacyHostIDPrefix + "h", true, http.StatusBadRequest},
		{"service mode, valid host id", "host-1", true, http.StatusCreated},
		{"ordinary worker, host id absent", nil, false, http.StatusCreated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, st := testServerWithStore()
			fields := map[string]any{"name": "w", "hostname": "h", "runtime": "none", "group": "llm", "service_mode": tt.serviceMode}
			if tt.hostID != nil {
				fields["host_id"] = tt.hostID
			}
			body, _ := json.Marshal(fields)
			w, env := doPost(t, srv, "/api/v1/workers/", string(body))
			if w.Code != tt.want {
				t.Fatalf("register = %d %s, want %d", w.Code, w.Body.String(), tt.want)
			}
			if tt.want != http.StatusBadRequest {
				return
			}
			if env.Error == nil || env.Error.Message != ServiceModeNeedsHostID {
				t.Errorf("error = %+v, want %q", env.Error, ServiceModeNeedsHostID)
			}
			if workers, err := st.ListWorkers(context.Background()); err != nil || len(workers) != 0 {
				t.Errorf("workers = %d, %v; want none registered", len(workers), err)
			}
		})
	}
}

// heartbeat sends body as a heartbeat of workerID and returns the decoded
// answer.
func heartbeat(t *testing.T, srv *Server, workerID, body string) model.HeartbeatResponse {
	t.Helper()
	w, env := doPut(t, srv, "/api/v1/workers/"+workerID+"/heartbeat", body)
	if w.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
	var resp model.HeartbeatResponse
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// R5: a heartbeat without running_tasks never touches a service; one with
// the field (empty list included) that omits it marks it missing, still
// RUNNING; one that lists it clears the mark.
func TestWorkerHeartbeat_ServiceRunningTasksField(t *testing.T) {
	srv, st := testServerWithStore()
	workerID := registerTestWorker(t, srv)
	seedOwnedTask(t, st, "task_svc", workerID, true, model.TaskStateRunning)

	for _, body := range []string{"", "{}", `{"running_tasks":null}`, `{"host":{"load1":1}}`} {
		heartbeat(t, srv, workerID, body)
		if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceMissingAt != nil {
			t.Fatalf("after heartbeat %q: %s, missing %v; want RUNNING, untouched", body, tk.State, tk.ServiceMissingAt)
		}
	}

	heartbeat(t, srv, workerID, `{"running_tasks":[]}`)
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ExternalID != workerID || tk.ServiceMissingAt == nil {
		t.Fatalf("after an empty list: %s on %q, missing %v; want RUNNING and marked missing", tk.State, tk.ExternalID, tk.ServiceMissingAt)
	}

	heartbeat(t, srv, workerID, `{"running_tasks":["task_svc"]}`)
	if tk := mustTask(t, st, "task_svc"); tk.State != model.TaskStateRunning || tk.ServiceMissingAt != nil {
		t.Fatalf("after listing it: %s, missing %v; want RUNNING, mark cleared", tk.State, tk.ServiceMissingAt)
	}
}

// R5: a listed service the server has ended is in cancel_tasks; a listed
// terminal ordinary task and a running service are not.
func TestWorkerHeartbeat_CancelsEndedService(t *testing.T) {
	srv, st := testServerWithStore()
	workerID := registerTestWorker(t, srv)
	seedOwnedTask(t, st, "svc_running", workerID, true, model.TaskStateRunning)
	seedOwnedTask(t, st, "svc_failed", workerID, true, model.TaskStateFailed)
	seedOwnedTask(t, st, "batch_failed", workerID, false, model.TaskStateFailed)

	resp := heartbeat(t, srv, workerID, `{"running_tasks":["svc_running","svc_failed","batch_failed"]}`)
	if len(resp.CancelTasks) != 1 || resp.CancelTasks[0] != "svc_failed" {
		t.Errorf("cancel_tasks = %v, want [svc_failed]", resp.CancelTasks)
	}
}

// R7: a FAILED submission with a service task is not retried (409), whatever
// its workflow holds now, even when the workflow row is gone; without a
// service task a missing workflow is still 404.
func TestRetrySubmission_ServiceTaskRefused(t *testing.T) {
	for _, withService := range []bool{true, false} {
		name := "no service task: 404"
		if withService {
			name = "service task: 409"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			srv, st, _ := newManagedGroupServer(t)
			now := time.Now().UTC()
			sub := &model.Submission{
				ID: "sub_review", WorkflowID: "wf_gone", WorkflowName: "gone", State: model.SubmissionStateFailed,
				SubmittedBy: "admin", Inputs: map[string]any{}, Outputs: map[string]any{},
				Labels: map[string]string{}, CreatedAt: now, CompletedAt: &now,
			}
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatal(err)
			}
			seedOwnedTask(t, st, "task_failed", "wrk_x", withService, model.TaskStateFailed)

			w, env := mgDo(t, srv, "PUT", "/api/v1/submissions/"+sub.ID+"/retry", "", adminAuthToken)
			if withService {
				if w.Code != http.StatusConflict || env.Error == nil || env.Error.Message != model.ServiceNotRerun {
					t.Fatalf("retry = %d %+v, want 409 %q", w.Code, env.Error, model.ServiceNotRerun)
				}
			} else if w.Code != http.StatusNotFound {
				t.Fatalf("retry = %d %s, want 404 (workflow gone)", w.Code, w.Body.String())
			}
			if got, err := st.GetSubmission(ctx, sub.ID); err != nil || got == nil || got.State != model.SubmissionStateFailed {
				t.Errorf("submission = %+v, %v; want unchanged FAILED", got, err)
			}
			if tk := mustTask(t, st, "task_failed"); tk.State != model.TaskStateFailed {
				t.Errorf("task = %s, want FAILED", tk.State)
			}
		})
	}
}

// R8: with worker auth on, service_mode is accepted only from a key whose
// groups name the worker's group; with worker auth off nothing changes.
func TestRegisterWorker_ServiceModeKey(t *testing.T) {
	register := func(t *testing.T, srv *Server, secret, group string, serviceMode bool) (int, envelope) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": "w", "hostname": "h", "runtime": "none", "group": group, "service_mode": serviceMode,
			"host_id": "host-1"})
		if secret == "" {
			w, env := doPost(t, srv, "/api/v1/workers/", string(body))
			return w.Code, env
		}
		w, env := keyedDo(t, srv, secret, "POST", "/api/v1/workers/", string(body))
		return w.Code, env
	}
	serviceModeOf := func(t *testing.T, env envelope) bool {
		t.Helper()
		var wk model.Worker
		if err := json.Unmarshal(env.Data, &wk); err != nil {
			t.Fatal(err)
		}
		return wk.ServiceMode
	}

	t.Run("key naming the group", func(t *testing.T) {
		srv, _ := keyedServer(t)
		code, env := register(t, srv, "secretG1", "g1", true)
		if code != http.StatusCreated || !serviceModeOf(t, env) {
			t.Fatalf("register = %d %+v, want 201 in service mode", code, env.Error)
		}
	})
	t.Run("key with an empty group list", func(t *testing.T) {
		srv, st := keyedServer(t)
		code, env := register(t, srv, "secretA", "g1", true)
		if code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
			!strings.Contains(env.Error.Message, `explicitly name group "g1"`) {
			t.Fatalf("register = %d %+v, want 403 naming the rule", code, env.Error)
		}
		if workers, err := st.ListWorkers(context.Background()); err != nil || len(workers) != 0 {
			t.Errorf("workers = %d, %v; want none registered", len(workers), err)
		}
		// The same key still registers an ordinary worker.
		if code, env := register(t, srv, "secretA", "g1", false); code != http.StatusCreated {
			t.Errorf("ordinary register = %d %+v, want 201", code, env.Error)
		}
	})
	t.Run("worker auth off", func(t *testing.T) {
		srv, _ := testServerWithStore()
		code, env := register(t, srv, "", "llm", true)
		if code != http.StatusCreated || !serviceModeOf(t, env) {
			t.Fatalf("register = %d %+v, want 201 in service mode", code, env.Error)
		}
	})
}
