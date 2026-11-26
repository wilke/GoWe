package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/model"
)

// The UI submission path runs the same worker-group fast-fail as the API
// (#261): a group the user may not use is refused with a visible error.
func TestHandleSubmissionCreatePost_GroupPolicy(t *testing.T) {
	policy := loadUIGroupPolicy(t, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}})

	tests := []struct {
		name      string
		policy    *groupacl.Policy
		user      string
		formGroup string
		stepGroup string
		wantDeny  bool
	}{
		{"label: unlisted user refused", policy, "mallory", "gpu", "", true},
		{"step hint: unlisted user refused", policy, "mallory", "", "gpu", true},
		{"label: listed user accepted", policy, "alice", "gpu", "", false},
		{"unlisted group open", policy, "mallory", "esmfold", "", false},
		{"no policy", nil, "mallory", "gpu", "gpu", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			u := New(st, slog.Default(), Config{InputValidation: validate.ModeOff, GroupPolicy: tt.policy})
			now := time.Now().UTC()
			step := model.Step{ID: "s"}
			if tt.stepGroup != "" {
				step.Hints = &model.StepHints{WorkerGroup: tt.stepGroup}
			}
			wf := &model.Workflow{ID: "wf_grp", Name: "grp-wf", Class: "Workflow", CWLVersion: "v1.2",
				Steps: []model.Step{step}, CreatedAt: now, UpdatedAt: now}
			if err := st.CreateWorkflow(t.Context(), wf); err != nil {
				t.Fatal(err)
			}
			sess := &model.Session{ID: "s1", Username: tt.user, Role: "user"}
			form := url.Values{}
			form.Set("workflow_id", wf.ID)
			if tt.formGroup != "" {
				form.Set("worker_group", tt.formGroup)
			}
			rec := postSubmissionCreate(t, u, sess, form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status %d", rec.Code)
			}
			loc := rec.Header().Get("Location")
			if !tt.wantDeny {
				if !strings.HasPrefix(loc, "/submissions/sub_") {
					t.Errorf("must accept: %s", loc)
				}
				return
			}
			if !strings.Contains(loc, "/submissions/new") {
				t.Fatalf("must refuse: %s", loc)
			}
			q, err := url.Parse(loc)
			if err != nil {
				t.Fatal(err)
			}
			want := `FORBIDDEN: user "mallory" may not use worker group "gpu"`
			if got := q.Query().Get("error"); got != want {
				t.Errorf("error = %q, want %q", got, want)
			}
		})
	}
}

// --- Re-queue handlers: Resume, RecomputeFailed, TaskRecompute ---

// loadUIGroupPolicy stores rules in a database of its own and returns a live
// policy loaded from it (no server admins).
func loadUIGroupPolicy(t *testing.T, rules ...*model.WorkerGroupPolicy) *groupacl.Policy {
	t.Helper()
	st := setupTestStore(t)
	t.Cleanup(func() { st.Close() })
	for _, r := range rules {
		if err := st.UpsertWorkerGroupPolicy(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	p := groupacl.New()
	if err := p.Reload(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	return p
}

// seedFailedTaskIn creates a FAILED task with retries exhausted on subID,
// run by exec in worker group group ("" = no runtime hints at all).
func seedFailedTaskIn(t *testing.T, st *store.SQLiteStore, taskID, subID string, exec model.ExecutorType, group string) {
	t.Helper()
	now := time.Now().UTC()
	task := &model.Task{
		ID:           taskID,
		SubmissionID: subID,
		StepID:       "step1",
		State:        model.TaskStateFailed,
		ExecutorType: exec,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
		RetryCount:   3,
		MaxRetries:   3,
		Stderr:       "stale stderr",
		StartedAt:    &now,
		CompletedAt:  &now,
	}
	if group != "" {
		task.RuntimeHints = &model.RuntimeHints{WorkerGroup: group}
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task %s: %v", taskID, err)
	}
}

// callRequeue runs one of the re-queue handlers ("resume",
// "recompute-failed", "task-recompute") as sess.
func callRequeue(t *testing.T, u *UI, handler, subID, taskID string, sess *model.Session) *httptest.ResponseRecorder {
	t.Helper()
	target := "/submissions/" + subID + "/" + handler
	if handler == "task-recompute" {
		target = "/submissions/" + subID + "/tasks/" + taskID + "/recompute"
	}
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.SetPathValue("id", subID)
	req.SetPathValue("tid", taskID)
	req = withSession(req, sess)
	w := httptest.NewRecorder()
	switch handler {
	case "resume":
		u.HandleSubmissionResume(w, req)
	case "recompute-failed":
		u.HandleRecomputeFailed(w, req)
	case "task-recompute":
		u.HandleTaskRecompute(w, req)
	default:
		t.Fatalf("unknown handler %q", handler)
	}
	return w
}

func taskStateOf(t *testing.T, st *store.SQLiteStore, id string) model.TaskState {
	t.Helper()
	task, err := st.GetTask(context.Background(), id)
	if err != nil || task == nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return task.State
}

func submissionStateOf(t *testing.T, st *store.SQLiteStore, id string) model.SubmissionState {
	t.Helper()
	sub, err := st.GetSubmission(context.Background(), id)
	if err != nil || sub == nil {
		t.Fatalf("get submission %s: %v", id, err)
	}
	return sub.State
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// F2: RecomputeFailed and TaskRecompute let only the submission's owner or
// an admin act, like Resume.
func TestRecomputeHandlers_OwnerOrAdmin(t *testing.T) {
	sessions := []struct {
		name       string
		sess       *model.Session
		wantStatus int
	}{
		{"another user refused", &model.Session{ID: "s2", Username: "mallory", Role: "user"}, http.StatusForbidden},
		{"owner allowed", &model.Session{ID: "s1", Username: testWSUser, Role: "user"}, http.StatusOK},
		{"admin allowed", &model.Session{ID: "s3", Username: "root", Role: string(model.RoleAdmin)}, http.StatusOK},
	}
	for _, handler := range []string{"recompute-failed", "task-recompute"} {
		for _, s := range sessions {
			t.Run(handler+": "+s.name, func(t *testing.T) {
				st := setupTestStore(t)
				defer st.Close()
				sub := seedRecomputeTestSubmission(t, st, "sub_owner", model.SubmissionStateFailed, testWSUser)
				task := seedFailedTask(t, st, "task_owner", sub.ID, "")

				w := callRequeue(t, newTestUI(st), handler, sub.ID, task.ID, s.sess)
				if w.Code != s.wantStatus {
					t.Fatalf("status = %d, want %d, body=%s", w.Code, s.wantStatus, w.Body.String())
				}
				wantTask, wantSub := model.TaskStateQueued, model.SubmissionStateRunning
				if s.wantStatus == http.StatusForbidden {
					wantTask, wantSub = model.TaskStateFailed, model.SubmissionStateFailed
				}
				if got := taskStateOf(t, st, task.ID); got != wantTask {
					t.Errorf("task state = %s, want %s", got, wantTask)
				}
				if got := submissionStateOf(t, st, sub.ID); got != wantSub {
					t.Errorf("submission state = %s, want %s", got, wantSub)
				}
			})
		}
	}
}

// F1: TaskRecompute re-checks the worker-group policy before a FAILED worker
// task goes back to QUEUED.
func TestHandleTaskRecompute_GroupPolicy(t *testing.T) {
	policy := loadUIGroupPolicy(t,
		&model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}},
		&model.WorkerGroupPolicy{Group: "default", Users: []string{"alice"}})
	tests := []struct {
		name       string
		user       string
		exec       model.ExecutorType
		group      string
		wantReason string // "" = requeued
	}{
		{"unlisted submitter refused", "mallory", model.ExecutorTypeWorker, "gpu", `submitter "mallory" may not use worker group "gpu"`},
		{"anonymous submitter refused", "anonymous", model.ExecutorTypeWorker, "gpu", `submitter "anonymous" may not use worker group "gpu"`},
		{"no group on the task: default group checked", "mallory", model.ExecutorTypeWorker, "", `submitter "mallory" may not use worker group "default"`},
		{"listed submitter requeued", "alice", model.ExecutorTypeWorker, "gpu", ""},
		{"unlisted group open", "mallory", model.ExecutorTypeWorker, "esmfold", ""},
		{"non-worker task not checked", "mallory", model.ExecutorTypeLocal, "gpu", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			sub := seedRecomputeTestSubmission(t, st, "sub_grp_tr", model.SubmissionStateFailed, tt.user)
			seedFailedTaskIn(t, st, "task_grp_tr", sub.ID, tt.exec, tt.group)
			u := New(st, quietLogger(), Config{GroupPolicy: policy})

			w := callRequeue(t, u, "task-recompute", sub.ID, "task_grp_tr", &model.Session{ID: "s1", Username: tt.user, Role: "user"})
			if tt.wantReason == "" {
				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
				}
				if got := taskStateOf(t, st, "task_grp_tr"); got != model.TaskStateQueued {
					t.Errorf("task state = %s, want QUEUED", got)
				}
				return
			}
			if w.Code != http.StatusForbidden || w.Body.String() != tt.wantReason {
				t.Fatalf("response = %d %q, want 403 %q", w.Code, w.Body.String(), tt.wantReason)
			}
			if got := taskStateOf(t, st, "task_grp_tr"); got != model.TaskStateFailed {
				t.Errorf("task state = %s, want unchanged FAILED", got)
			}
			if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateFailed {
				t.Errorf("submission state = %s, want unchanged FAILED", got)
			}
		})
	}
}

// F1: Resume and RecomputeFailed skip and report the denied tasks and
// requeue the allowed ones; when every task is denied nothing changes.
func TestBulkRequeue_GroupPolicy(t *testing.T) {
	policy := loadUIGroupPolicy(t, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}})
	const reason = `submitter "mallory" may not use worker group "gpu"`
	owner := &model.Session{ID: "s1", Username: "mallory", Role: "user"}
	for _, h := range []struct{ handler, verb string }{{"resume", "requeued"}, {"recompute-failed", "recomputed"}} {
		t.Run(h.handler+": denied skipped, allowed requeued", func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			sub := seedRecomputeTestSubmission(t, st, "sub_grp_bulk", model.SubmissionStateFailed, "mallory")
			seedFailedTaskIn(t, st, "task_gpu_1", sub.ID, model.ExecutorTypeWorker, "gpu")
			seedFailedTaskIn(t, st, "task_gpu_2", sub.ID, model.ExecutorTypeWorker, "gpu")
			seedFailedTaskIn(t, st, "task_open", sub.ID, model.ExecutorTypeWorker, "esmfold")
			u := New(st, quietLogger(), Config{GroupPolicy: policy})

			w := callRequeue(t, u, h.handler, sub.ID, "", owner)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
			}
			if got, want := w.Body.String(), h.verb+" 1, skipped 0, denied 2: "+reason; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			for _, id := range []string{"task_gpu_1", "task_gpu_2"} {
				if got := taskStateOf(t, st, id); got != model.TaskStateFailed {
					t.Errorf("%s state = %s, want unchanged FAILED", id, got)
				}
			}
			if got := taskStateOf(t, st, "task_open"); got != model.TaskStateQueued {
				t.Errorf("task_open state = %s, want QUEUED", got)
			}
			if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateRunning {
				t.Errorf("submission state = %s, want RUNNING", got)
			}
		})
		t.Run(h.handler+": every task denied, nothing changes", func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			sub := seedRecomputeTestSubmission(t, st, "sub_grp_alldenied", model.SubmissionStateFailed, "mallory")
			seedFailedTaskIn(t, st, "task_gpu", sub.ID, model.ExecutorTypeWorker, "gpu")
			u := New(st, quietLogger(), Config{GroupPolicy: policy})

			w := callRequeue(t, u, h.handler, sub.ID, "", owner)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
			}
			if got, want := w.Body.String(), h.verb+" 0, skipped 0, denied 1: "+reason; got != want {
				t.Errorf("body = %q, want %q", got, want)
			}
			if got := taskStateOf(t, st, "task_gpu"); got != model.TaskStateFailed {
				t.Errorf("task state = %s, want unchanged FAILED", got)
			}
			if got := submissionStateOf(t, st, sub.ID); got != model.SubmissionStateFailed {
				t.Errorf("submission state = %s, want unchanged FAILED", got)
			}
		})
	}
}
