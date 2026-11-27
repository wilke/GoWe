package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/internal/tokencheck"
	"github.com/me/gowe/pkg/model"
)

// A service task (gowe:Service) is never started a second time: a worker
// holding one is not removed, a service workflow's submission is not
// retried, and cwl:requirements is refused at submission.

// seedRunningServiceTask stores a RUNNING worker service task of group g
// checked out by workerID.
func seedRunningServiceTask(t *testing.T, srv *Server, id, workerID string) {
	t.Helper()
	started := time.Now().UTC()
	if err := srv.store.CreateTask(context.Background(), &model.Task{
		ID: id, SubmissionID: "sub_svc", StepID: "s", State: model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker, ExternalID: workerID, StartedAt: &started,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "g", Service: &model.ServiceSpec{PortInput: "port"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// Every REST route that removes a worker answers 409 for a worker that
// still holds a RUNNING service, and changes nothing.
func TestWorkerRemoval_RefusedWithRunningService(t *testing.T) {
	const want = "worker still holds 1 running service(s); cancel their submissions first"
	routes := []struct {
		name string
		do   func(t *testing.T, srv *Server) (*httptest.ResponseRecorder, envelope)
	}{
		{"worker deregistration", func(t *testing.T, srv *Server) (*httptest.ResponseRecorder, envelope) {
			return peerDo(t, srv, "DELETE", "/api/v1/workers/wrk_svc", "", "192.0.2.9:1")
		}},
		{"admin removal", func(t *testing.T, srv *Server) (*httptest.ResponseRecorder, envelope) {
			return wgDo(t, srv, "DELETE", "/api/v1/admin/workers/wrk_svc", "", adminAuthToken)
		}},
		{"group manager removal", func(t *testing.T, srv *Server) (*httptest.ResponseRecorder, envelope) {
			return mgDo(t, srv, "DELETE", managedGroupsPath+"/g/workers/wrk_svc", "", mgrToken)
		}},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			ctx := context.Background()
			srv, st, _ := newManagedGroupServer(t)
			seedWorker(t, st, "wrk_svc", "svc", "g", model.WorkerStateOffline, time.Now().UTC().Add(-time.Hour), "", "")
			seedRunningServiceTask(t, srv, "task_svc", "wrk_svc")

			w, env := rt.do(t, srv)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
			}
			if env.Error == nil || env.Error.Code != model.ErrConflict || env.Error.Message != want {
				t.Errorf("error = %+v, want CONFLICT %q", env.Error, want)
			}
			if wk, err := st.GetWorker(ctx, "wrk_svc"); err != nil || wk == nil {
				t.Errorf("worker = (%v, %v), want kept", wk, err)
			}
			if task, _ := st.GetTask(ctx, "task_svc"); task == nil || task.State != model.TaskStateRunning || task.ExternalID != "wrk_svc" {
				t.Errorf("service task = %+v, want RUNNING on wrk_svc", task)
			}
		})
	}
}

// registerServiceWorkflow registers svcDoc (group g) as an admin and returns
// the stored workflow.
func registerServiceWorkflow(t *testing.T, srv *Server) model.Workflow {
	t.Helper()
	w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), adminAuthToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register service workflow: %d %s", w.Code, w.Body.String())
	}
	wf, _ := createdWorkflow(t, env)
	return wf
}

// A FAILED submission of a service workflow is not retried (409).
func TestRetrySubmission_ServiceWorkflowRefused(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	wf := registerServiceWorkflow(t, srv)
	now := time.Now().UTC()
	sub := &model.Submission{
		ID: "sub_svc_failed", WorkflowID: wf.ID, WorkflowName: wf.Name, State: model.SubmissionStateFailed,
		SubmittedBy: "admin", Inputs: map[string]any{"port": 8000}, Outputs: map[string]any{},
		Labels: map[string]string{}, CreatedAt: now, CompletedAt: &now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatal(err)
	}

	w, env := mgDo(t, srv, "PUT", "/api/v1/submissions/"+sub.ID+"/retry", "", adminAuthToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("retry: %d %s, want 409", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrConflict || env.Error.Message != model.ServiceNotRerun {
		t.Errorf("error = %+v, want CONFLICT %q", env.Error, model.ServiceNotRerun)
	}
	if got, err := st.GetSubmission(ctx, sub.ID); err != nil || got == nil || got.State != model.SubmissionStateFailed {
		t.Errorf("submission = %+v, %v; want unchanged FAILED", got, err)
	}
}

// A submission of a service workflow whose inputs contain cwl:requirements
// (whatever its value) is refused with 400; without the key it is accepted.
func TestCreateSubmission_ServiceRefusesJobRequirements(t *testing.T) {
	srv, _, _ := newManagedGroupServer(t)
	wf := registerServiceWorkflow(t, srv)
	submit := func(t *testing.T, inputs map[string]any) (*httptest.ResponseRecorder, envelope) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"workflow_id": wf.ID, "inputs": inputs})
		if err != nil {
			t.Fatal(err)
		}
		return mgDo(t, srv, "POST", "/api/v1/submissions/", string(body), mgrToken)
	}

	for _, tt := range []struct {
		name string
		reqs any
	}{
		{"DockerRequirement", []any{map[string]any{"class": "DockerRequirement", "dockerPull": "other.sif"}}},
		{"empty list", []any{}},
		{"null", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, env := submit(t, map[string]any{"port": 8000, "cwl:requirements": tt.reqs})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("submit: %d %s, want 400", w.Code, w.Body.String())
			}
			if env.Error == nil || env.Error.Message != tokencheck.ServiceJobRequirementsRefused {
				t.Errorf("error = %+v, want %q", env.Error, tokencheck.ServiceJobRequirementsRefused)
			}
		})
	}

	t.Run("without the key: accepted", func(t *testing.T) {
		if w, _ := submit(t, map[string]any{"port": 8000}); w.Code != http.StatusCreated {
			t.Fatalf("submit: %d %s, want 201", w.Code, w.Body.String())
		}
	})
}
