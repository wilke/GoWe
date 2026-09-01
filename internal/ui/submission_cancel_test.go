package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func seedCancelTestSubmission(t *testing.T, st *store.SQLiteStore, submittedBy string) *model.Submission {
	t.Helper()
	ctx := context.Background()
	wf := &model.Workflow{
		ID:         "wf_ui_cancel",
		Name:       "ui-cancel-test",
		CWLVersion: "v1.2",
		RawCWL:     "cwlVersion: v1.2\n",
	}
	// A workflow row is created at most once across subtests sharing a store;
	// tests here each get a fresh store, so this is always the first write.
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := &model.Submission{
		ID:          "sub_ui_cancel",
		WorkflowID:  wf.ID,
		State:       model.SubmissionStateRunning,
		Inputs:      map[string]any{},
		SubmittedBy: submittedBy,
		CreatedAt:   time.Now().UTC().Add(-time.Minute),
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	return sub
}

func TestHandleSubmissionCancel_CancelsTasksAndFansOutToChildren(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedCancelTestSubmission(t, st, testWSUser)

	// A plain in-flight task directly on the submission.
	plainTask := &model.Task{
		ID:           "task_ui_plain",
		SubmissionID: sub.ID,
		StepID:       "step1",
		State:        model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeWorker,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
	}
	if err := st.CreateTask(ctx, plainTask); err != nil {
		t.Fatalf("create plain task: %v", err)
	}

	// A subworkflow proxy task with a RUNNING child submission — proves the
	// fan-out (the part the old direct-finalize handler never did) reaches
	// descendants.
	proxy := &model.Task{
		ID:           "task_ui_proxy",
		SubmissionID: sub.ID,
		StepID:       "subwf",
		State:        model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeSubworkflow,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
	}
	if err := st.CreateTask(ctx, proxy); err != nil {
		t.Fatalf("create proxy task: %v", err)
	}
	child := &model.Submission{
		ID:           "sub_ui_child",
		WorkflowID:   sub.WorkflowID,
		State:        model.SubmissionStateRunning,
		Inputs:       map[string]any{},
		SubmittedBy:  sub.SubmittedBy,
		ParentTaskID: proxy.ID,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateSubmission(ctx, child); err != nil {
		t.Fatalf("create child submission: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(st, logger, Config{})
	reg := metrics.NewRegistry(metrics.Config{})
	u.WithMetrics(reg)

	req := httptest.NewRequest(http.MethodPut, "/submissions/"+sub.ID+"/cancel", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionCancel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateCancelled {
		t.Errorf("submission state = %s, want CANCELLED", gotSub.State)
	}

	gotTask, err := st.GetTask(ctx, plainTask.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get plain task: %v", err)
	}
	if gotTask.State != model.TaskStateSkipped {
		t.Errorf("plain task state = %s, want SKIPPED — the pre-cancelseq UI handler never did this (#185)", gotTask.State)
	}

	gotProxy, err := st.GetTask(ctx, proxy.ID)
	if err != nil || gotProxy == nil {
		t.Fatalf("get proxy task: %v", err)
	}
	if gotProxy.State != model.TaskStateSkipped {
		t.Errorf("proxy task state = %s, want SKIPPED", gotProxy.State)
	}

	gotChild, err := st.GetSubmission(ctx, child.ID)
	if err != nil || gotChild == nil {
		t.Fatalf("get child submission: %v", err)
	}
	if gotChild.State != model.SubmissionStateCancelled {
		t.Errorf("child submission state = %s, want CANCELLED — the pre-cancelseq UI handler never fanned out (#185)", gotChild.State)
	}

	assertCancelledWallSamples(t, reg, 2) // sub + child
}

// assertCancelledWallSamples reads gowe_submission_wall_seconds{outcome=cancelled}
// through the public Gatherer so this test never needs the Registry's
// unexported fields.
func assertCancelledWallSamples(t *testing.T, reg *metrics.Registry, want uint64) {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gowe_submission_wall_seconds" {
			continue
		}
		var got uint64
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == "cancelled" {
					got += m.GetHistogram().GetSampleCount()
				}
			}
		}
		if got != want {
			t.Errorf("gowe_submission_wall_seconds{outcome=cancelled} samples = %d, want %d", got, want)
		}
		return
	}
	t.Fatal("gowe_submission_wall_seconds metric family not found")
}

func TestHandleSubmissionCancel_AlreadyTerminal400(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	sub := seedCancelTestSubmission(t, st, testWSUser)
	sub.State = model.SubmissionStateCompleted
	now := time.Now().UTC()
	sub.CompletedAt = &now
	if err := st.UpdateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("update submission: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(st, logger, Config{})

	req := httptest.NewRequest(http.MethodPut, "/submissions/"+sub.ID+"/cancel", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	w := httptest.NewRecorder()
	u.HandleSubmissionCancel(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", w.Code)
	}
}
