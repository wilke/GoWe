package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// The UI delete of a submission with a RUNNING service answers 409 and
// changes nothing (its other tasks are not cancelled); once the service has
// ended the submission is deleted.
func TestHandleSubmissionDelete_RunningService(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()
	sub := seedCancelTestSubmission(t, st, testWSUser)
	started := time.Now().UTC().Add(-time.Hour)
	for _, tk := range []*model.Task{
		{ID: "task_svc", SubmissionID: sub.ID, StepID: "s", State: model.TaskStateRunning,
			ExecutorType: model.ExecutorTypeWorker, ExternalID: "wrk_svc", StartedAt: &started,
			Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
			RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}}},
		{ID: "task_batch", SubmissionID: sub.ID, StepID: "b", State: model.TaskStateQueued,
			ExecutorType: model.ExecutorTypeWorker, Inputs: map[string]any{}, Outputs: map[string]any{},
			Job: map[string]any{}, ScatterIndex: -1},
	} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	del := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/submissions/"+sub.ID, nil)
		req.SetPathValue("id", sub.ID)
		req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
		rec := httptest.NewRecorder()
		newTestUI(st).HandleSubmissionDelete(rec, req)
		return rec
	}

	rec := del()
	if want := store.ErrSubmissionHasRunningService.Error(); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("response = %d %q, want 409 containing %q", rec.Code, rec.Body.String(), want)
	}
	if got, err := st.GetSubmission(ctx, sub.ID); err != nil || got == nil {
		t.Errorf("submission = (%v, %v), want kept", got, err)
	}
	if got := taskStateOf(t, st, "task_svc"); got != model.TaskStateRunning {
		t.Errorf("service task = %s, want RUNNING", got)
	}
	if got := taskStateOf(t, st, "task_batch"); got != model.TaskStateQueued {
		t.Errorf("batch task = %s, want QUEUED (nothing cancelled)", got)
	}

	svc, err := st.GetTask(ctx, "task_svc")
	if err != nil || svc == nil {
		t.Fatalf("GetTask = (%v, %v)", svc, err)
	}
	now := time.Now().UTC()
	svc.State, svc.CompletedAt = model.TaskStateFailed, &now
	if err := st.UpdateTask(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if rec := del(); rec.Code != http.StatusOK {
		t.Fatalf("delete after the end = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if got, _ := st.GetSubmission(ctx, sub.ID); got != nil {
		t.Error("submission still there")
	}
}

// checkoutAfterCheckStore checks the QUEUED service of group llm out to
// wrk_svc right after the delete handler's HasRunningService check, as a
// worker polling at that moment would.
type checkoutAfterCheckStore struct {
	store.Store
}

func (s *checkoutAfterCheckStore) HasRunningService(ctx context.Context, id string) (bool, error) {
	running, err := s.Store.HasRunningService(ctx, id)
	if err != nil || running {
		return running, err
	}
	if _, err := s.Store.CheckoutTask(ctx, "wrk_svc", "llm", model.RuntimeNone); err != nil {
		return false, err
	}
	return false, nil
}

// F5: a UI delete refused by the store after the handler's own cancel (a
// service checked out in between) completes the cancel: 409 saying the
// submission was cancelled and its service is stopping, the submission
// CANCELLED, the service RUNNING with a stop request, the other task SKIPPED.
func TestHandleSubmissionDelete_ServiceCheckedOutDuringDelete(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()
	sub := seedCancelTestSubmission(t, st, testWSUser)
	now := time.Now().UTC()
	if err := st.CreateWorker(ctx, &model.Worker{
		ID: "wrk_svc", Name: "wrk_svc", Hostname: "h", Group: "llm", State: model.WorkerStateOnline,
		Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now, ServiceMode: true, HostID: "host-1",
		Labels: map[string]string{}, Datasets: map[string]string{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []*model.Task{
		{ID: "task_svc", SubmissionID: sub.ID, StepID: "s", State: model.TaskStateQueued,
			ExecutorType: model.ExecutorTypeWorker, Inputs: map[string]any{}, Outputs: map[string]any{},
			Job: map[string]any{}, ScatterIndex: -1, CreatedAt: now.Add(-time.Minute),
			RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}}},
		{ID: "task_batch", SubmissionID: sub.ID, StepID: "b", State: model.TaskStateQueued,
			ExecutorType: model.ExecutorTypeWorker, Inputs: map[string]any{}, Outputs: map[string]any{},
			Job: map[string]any{}, ScatterIndex: -1, CreatedAt: now.Add(-time.Minute),
			RuntimeHints: &model.RuntimeHints{WorkerGroup: "llm"}},
	} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodDelete, "/submissions/"+sub.ID, nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	rec := httptest.NewRecorder()
	newTestUI(&checkoutAfterCheckStore{Store: st}).HandleSubmissionDelete(rec, req)

	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.DeleteRefusedServiceStopping) {
		t.Fatalf("response = %d %q, want 409 containing %q", rec.Code, rec.Body.String(), store.DeleteRefusedServiceStopping)
	}
	if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateCancelled {
		t.Errorf("submission = %s, want CANCELLED", got)
	}
	svc, err := st.GetTask(ctx, "task_svc")
	if err != nil || svc == nil {
		t.Fatalf("GetTask = (%v, %v)", svc, err)
	}
	if svc.State != model.TaskStateRunning || svc.ExternalID != "wrk_svc" || svc.ServiceStopRequestedAt == nil {
		t.Errorf("service = %s on %q (stop %v), want RUNNING on wrk_svc with a stop request", svc.State, svc.ExternalID, svc.ServiceStopRequestedAt)
	}
	if got := taskStateOf(t, st, "task_batch"); got != model.TaskStateSkipped {
		t.Errorf("batch task = %s, want SKIPPED", got)
	}
}
