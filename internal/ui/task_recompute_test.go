package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// seedRecomputeTestSubmission creates a workflow + submission in the given
// state for the Resume/RecomputeFailed/TaskRecompute handler tests (#187).
func seedRecomputeTestSubmission(t *testing.T, st *store.SQLiteStore, id string, subState model.SubmissionState, submittedBy string) *model.Submission {
	t.Helper()
	ctx := context.Background()
	wf := &model.Workflow{
		ID:         "wf_ui_recompute_" + id,
		Name:       "ui-recompute-test",
		CWLVersion: "v1.2",
		RawCWL:     "cwlVersion: v1.2\n",
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := &model.Submission{
		ID:          id,
		WorkflowID:  wf.ID,
		State:       subState,
		Inputs:      map[string]any{},
		SubmittedBy: submittedBy,
		CreatedAt:   time.Now().UTC().Add(-time.Minute),
	}
	if subState.IsTerminal() {
		now := time.Now().UTC()
		sub.CompletedAt = &now
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	return sub
}

// seedFailedTask creates a FAILED task (with stale bookkeeping fields, as a
// real failed run would leave) directly on subID, optionally attached to a
// step instance.
func seedFailedTask(t *testing.T, st *store.SQLiteStore, taskID, subID, stepInstanceID string) *model.Task {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	task := &model.Task{
		ID:             taskID,
		SubmissionID:   subID,
		StepID:         "step1",
		StepInstanceID: stepInstanceID,
		State:          model.TaskStateFailed,
		ExecutorType:   model.ExecutorTypeLocal,
		Inputs:         map[string]any{},
		Outputs:        map[string]any{},
		Job:            map[string]any{},
		ScatterIndex:   -1,
		RetryCount:     3,
		MaxRetries:     3,
		Stdout:         "stale stdout",
		Stderr:         "stale stderr",
		ExitCode:       intPtr(1),
		StartedAt:      &now,
		CompletedAt:    &now,
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create failed task: %v", err)
	}
	return task
}

func intPtr(v int) *int { return &v }

func newTestUI(st store.Store) *UI {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, logger, Config{})
}

// raceInjectingStore wraps a store.Store and, on the first GetTask/
// GetSubmission call touching raceTaskID, flips that task FAILED→RETRYING
// directly against the underlying store — simulating the scheduler's
// markRetries winning the CAS race — before returning the still-FAILED
// snapshot the caller's read observed. This reproduces the exact race #187
// guards against: a handler that read a task as FAILED must not clobber a
// state a concurrent writer already moved on from. Embedding store.Store
// delegates every other method unchanged.
type raceInjectingStore struct {
	store.Store
	raceTaskID string
	fired      bool
}

func (r *raceInjectingStore) fireRace(ctx context.Context) {
	if r.fired {
		return
	}
	r.fired = true
	if _, err := r.Store.CASTaskState(ctx, r.raceTaskID, model.TaskStateFailed, model.TaskStateRetrying); err != nil {
		panic(err)
	}
}

func (r *raceInjectingStore) GetTask(ctx context.Context, id string) (*model.Task, error) {
	task, err := r.Store.GetTask(ctx, id)
	if err != nil || task == nil || id != r.raceTaskID {
		return task, err
	}
	r.fireRace(ctx)
	return task, nil
}

func (r *raceInjectingStore) GetSubmission(ctx context.Context, id string) (*model.Submission, error) {
	sub, err := r.Store.GetSubmission(ctx, id)
	if err != nil || sub == nil {
		return sub, err
	}
	for _, t := range sub.Tasks {
		if t.ID == r.raceTaskID {
			r.fireRace(ctx)
			break
		}
	}
	return sub, err
}

// --- HandleTaskRecompute ---

func TestHandleTaskRecompute_HappyPath(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_tr_happy", model.SubmissionStateFailed, testWSUser)
	si := &model.StepInstance{ID: "si_tr_happy", SubmissionID: sub.ID, StepID: "step1", State: model.StepStateFailed}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}
	task := seedFailedTask(t, st, "task_tr_happy", sub.ID, si.ID)

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/tasks/"+task.ID+"/recompute", nil)
	req.SetPathValue("id", sub.ID)
	req.SetPathValue("tid", task.ID)
	w := httptest.NewRecorder()

	u.HandleTaskRecompute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("HX-Redirect") != "/submissions/"+sub.ID {
		t.Errorf("HX-Redirect = %q", w.Header().Get("HX-Redirect"))
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateQueued {
		t.Errorf("task state = %s, want QUEUED", gotTask.State)
	}
	if gotTask.RetryCount != 0 {
		t.Errorf("task retry_count = %d, want 0", gotTask.RetryCount)
	}
	if gotTask.Stdout != "" || gotTask.Stderr != "" || gotTask.ExitCode != nil {
		t.Errorf("task bookkeeping not cleared: stdout=%q stderr=%q exit_code=%v", gotTask.Stdout, gotTask.Stderr, gotTask.ExitCode)
	}
	if gotTask.StartedAt != nil || gotTask.CompletedAt != nil {
		t.Errorf("task timestamps not cleared: started=%v completed=%v", gotTask.StartedAt, gotTask.CompletedAt)
	}

	gotSI, err := st.GetStepInstance(ctx, si.ID)
	if err != nil || gotSI == nil {
		t.Fatalf("get step instance: %v", err)
	}
	if gotSI.State != model.StepStateDispatched {
		t.Errorf("step instance state = %s, want DISPATCHED", gotSI.State)
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateRunning {
		t.Errorf("submission state = %s, want RUNNING", gotSub.State)
	}
}

// TestHandleTaskRecompute_ConcurrentRetryingLeftUntouched simulates the
// scheduler's markRetries winning the FAILED→RETRYING CAS race between the
// handler's initial GetTask read and its guarded write. The handler must
// not clobber the task back to QUEUED with stale bookkeeping — the CAS
// write should be rejected and reported (409) instead.
func TestHandleTaskRecompute_ConcurrentRetryingLeftUntouched(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_tr_race", model.SubmissionStateFailed, testWSUser)
	task := seedFailedTask(t, st, "task_tr_race", sub.ID, "")

	// The handler's own GetTask read returns the task as FAILED (passing
	// its precheck); markRetries' CAS then fires against the real store
	// before the handler's guarded write runs, simulating the scheduler
	// winning the FAILED→RETRYING race in the window between the read and
	// the write.
	rs := &raceInjectingStore{Store: st, raceTaskID: task.ID}
	u := newTestUI(rs)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/tasks/"+task.ID+"/recompute", nil)
	req.SetPathValue("id", sub.ID)
	req.SetPathValue("tid", task.ID)
	w := httptest.NewRecorder()

	u.HandleTaskRecompute(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
	}
	if want := "task " + task.ID + " is no longer FAILED, skipped"; w.Body.String() != want {
		t.Errorf("body = %q, want %q", w.Body.String(), want)
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateRetrying {
		t.Errorf("task state = %s, want RETRYING (left untouched)", gotTask.State)
	}
	// The stale bookkeeping the handler would have written must not appear —
	// proof the CAS guard, not a full-row UpdateTask, made the write.
	if gotTask.RetryCount != 3 {
		t.Errorf("task retry_count = %d, want unchanged 3", gotTask.RetryCount)
	}
}

func TestHandleTaskRecompute_CancelledSubmissionRefused(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_tr_cancelled", model.SubmissionStateCancelled, testWSUser)
	task := seedFailedTask(t, st, "task_tr_cancelled", sub.ID, "")

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/tasks/"+task.ID+"/recompute", nil)
	req.SetPathValue("id", sub.ID)
	req.SetPathValue("tid", task.ID)
	w := httptest.NewRecorder()

	u.HandleTaskRecompute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateFailed {
		t.Errorf("task state = %s, want unchanged FAILED", gotTask.State)
	}
}

// --- HandleRecomputeFailed ---

func TestHandleRecomputeFailed_HappyPathCounts(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_rf_happy", model.SubmissionStateFailed, testWSUser)
	t1 := seedFailedTask(t, st, "task_rf_1", sub.ID, "")
	t2 := seedFailedTask(t, st, "task_rf_2", sub.ID, "")

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/recompute-failed", nil)
	req.SetPathValue("id", sub.ID)
	w := httptest.NewRecorder()

	u.HandleRecomputeFailed(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "recomputed 2, skipped 0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	for _, id := range []string{t1.ID, t2.ID} {
		gotTask, err := st.GetTask(ctx, id)
		if err != nil || gotTask == nil {
			t.Fatalf("get task %s: %v", id, err)
		}
		if gotTask.State != model.TaskStateQueued {
			t.Errorf("task %s state = %s, want QUEUED", id, gotTask.State)
		}
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateRunning {
		t.Errorf("submission state = %s, want RUNNING", gotSub.State)
	}
}

// TestHandleRecomputeFailed_ConcurrentRetryingSkippedNotClobbered seeds two
// FAILED tasks, has the scheduler win the CAS race on one of them (as if
// markRetries ran between GetSubmission and the reset loop), and asserts
// the handler resets only the untouched task while leaving the raced one
// alone and still succeeding overall (200, redirect intact).
func TestHandleRecomputeFailed_ConcurrentRetryingSkippedNotClobbered(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_rf_race", model.SubmissionStateFailed, testWSUser)
	clean := seedFailedTask(t, st, "task_rf_clean", sub.ID, "")
	raced := seedFailedTask(t, st, "task_rf_raced", sub.ID, "")

	// The handler's GetSubmission read snapshots both tasks as FAILED (so
	// the loop does not pre-filter "raced" out); markRetries' CAS then
	// fires against the real store before the loop reaches its guarded
	// write for "raced", simulating the scheduler winning that task's
	// FAILED→RETRYING race in the window between the snapshot and the
	// per-task write.
	rs := &raceInjectingStore{Store: st, raceTaskID: raced.ID}
	u := newTestUI(rs)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/recompute-failed", nil)
	req.SetPathValue("id", sub.ID)
	w := httptest.NewRecorder()

	u.HandleRecomputeFailed(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "recomputed 1, skipped 1"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	gotClean, err := st.GetTask(ctx, clean.ID)
	if err != nil || gotClean == nil {
		t.Fatalf("get clean task: %v", err)
	}
	if gotClean.State != model.TaskStateQueued {
		t.Errorf("clean task state = %s, want QUEUED", gotClean.State)
	}

	gotRaced, err := st.GetTask(ctx, raced.ID)
	if err != nil || gotRaced == nil {
		t.Fatalf("get raced task: %v", err)
	}
	if gotRaced.State != model.TaskStateRetrying {
		t.Errorf("raced task state = %s, want unchanged RETRYING (skipped, not clobbered)", gotRaced.State)
	}
	if gotRaced.RetryCount != 3 {
		t.Errorf("raced task retry_count = %d, want unchanged 3", gotRaced.RetryCount)
	}
}

// TestHandleRecomputeFailed_AllRacedAway_SubmissionStaysFailed covers the
// edge of the terminal-submission reactivation gate: when every FAILED task
// loses its CAS race, recomputeCount stays 0, so the submission must not be
// bounced back to RUNNING with nothing actually re-queued.
func TestHandleRecomputeFailed_AllRacedAway_SubmissionStaysFailed(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_rf_allraced", model.SubmissionStateFailed, testWSUser)
	raced := seedFailedTask(t, st, "task_rf_allraced", sub.ID, "")

	rs := &raceInjectingStore{Store: st, raceTaskID: raced.ID}
	u := newTestUI(rs)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/recompute-failed", nil)
	req.SetPathValue("id", sub.ID)
	w := httptest.NewRecorder()

	u.HandleRecomputeFailed(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "recomputed 0, skipped 1"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	gotRaced, err := st.GetTask(ctx, raced.ID)
	if err != nil || gotRaced == nil {
		t.Fatalf("get raced task: %v", err)
	}
	if gotRaced.State != model.TaskStateRetrying {
		t.Errorf("raced task state = %s, want unchanged RETRYING", gotRaced.State)
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateFailed {
		t.Errorf("submission state = %s, want unchanged FAILED (nothing was actually recomputed)", gotSub.State)
	}
}

func TestHandleRecomputeFailed_CancelledSubmissionRefused(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_rf_cancelled", model.SubmissionStateCancelled, testWSUser)
	task := seedFailedTask(t, st, "task_rf_cancelled", sub.ID, "")

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/recompute-failed", nil)
	req.SetPathValue("id", sub.ID)
	w := httptest.NewRecorder()

	u.HandleRecomputeFailed(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateFailed {
		t.Errorf("task state = %s, want unchanged FAILED", gotTask.State)
	}
}

// --- HandleSubmissionResume ---

func TestHandleSubmissionResume_HappyPath(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_res_happy", model.SubmissionStateFailed, testWSUser)
	task := seedFailedTask(t, st, "task_res_happy", sub.ID, "")

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/resume", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "requeued 1, skipped 0"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateQueued {
		t.Errorf("task state = %s, want QUEUED", gotTask.State)
	}

	gotSub, err := st.GetSubmission(ctx, sub.ID)
	if err != nil || gotSub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if gotSub.State != model.SubmissionStateRunning {
		t.Errorf("submission state = %s, want RUNNING", gotSub.State)
	}
}

// TestHandleSubmissionResume_ConcurrentRetryingLeftUntouched mirrors the
// TaskRecompute race test at the bulk-resume handler: a task the scheduler
// has already claimed for RETRYING must survive a resume request untouched.
func TestHandleSubmissionResume_ConcurrentRetryingLeftUntouched(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_res_race", model.SubmissionStateFailed, testWSUser)
	raced := seedFailedTask(t, st, "task_res_raced", sub.ID, "")

	// See TestHandleRecomputeFailed_ConcurrentRetryingSkippedNotClobbered:
	// the handler's GetSubmission snapshot sees "raced" as FAILED, and
	// markRetries' CAS fires before the handler's guarded write for it.
	rs := &raceInjectingStore{Store: st, raceTaskID: raced.ID}
	u := newTestUI(rs)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/resume", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionResume(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "requeued 0, skipped 1"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	gotRaced, err := st.GetTask(ctx, raced.ID)
	if err != nil || gotRaced == nil {
		t.Fatalf("get raced task: %v", err)
	}
	if gotRaced.State != model.TaskStateRetrying {
		t.Errorf("raced task state = %s, want unchanged RETRYING (skipped, not clobbered)", gotRaced.State)
	}
}

// TestHandleSubmissionResume_CancelledSubmissionRefused documents that a
// CANCELLED submission is refused by Resume's existing "must be exactly
// FAILED" precondition (stricter than, and thus subsuming, the explicit
// not-CANCELLED gate added to RecomputeFailed/TaskRecompute).
func TestHandleSubmissionResume_CancelledSubmissionRefused(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_res_cancelled", model.SubmissionStateCancelled, testWSUser)
	task := seedFailedTask(t, st, "task_res_cancelled", sub.ID, "")

	u := newTestUI(st)
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/resume", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionResume(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}

	gotTask, err := st.GetTask(ctx, task.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("get task: %v", err)
	}
	if gotTask.State != model.TaskStateFailed {
		t.Errorf("task state = %s, want unchanged FAILED", gotTask.State)
	}
}
