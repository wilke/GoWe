package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// getTaskLogsPage renders the task logs page of task taskID of subID as the
// submission's owner.
func getTaskLogsPage(t *testing.T, u *UI, subID, taskID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/submissions/"+subID+"/tasks/"+taskID+"/logs", nil)
	req.SetPathValue("id", subID)
	req.SetPathValue("tid", taskID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()
	u.HandleTaskLogs(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestHandleTaskLogs_EarlierAttempts(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_att_view", model.SubmissionStateFailed, testWSUser)
	task := seedFailedTask(t, st, "task_att_view", sub.ID, "")
	u := newTestUI(st)

	// No earlier attempt: no section.
	if body := getTaskLogsPage(t, u, sub.ID, task.ID); strings.Contains(body, "Earlier attempts") {
		t.Errorf("page shows the earlier attempts section without attempts")
	}

	// The first attempt failed with this stderr and was retried; the task now
	// holds the second attempt's output.
	task.Stderr = "attempt one failed <b>badly</b>"
	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("update task: %v", err)
	}
	next := *task
	next.Stderr = "attempt two output"
	if applied, err := st.ArchiveAndResetTask(ctx, &next, model.TaskStateFailed, model.AttemptReasonRetry); err != nil || !applied {
		t.Fatalf("ArchiveAndResetTask = (%v, %v), want (true, nil)", applied, err)
	}

	body := getTaskLogsPage(t, u, sub.ID, task.ID)
	for _, want := range []string{
		"Earlier attempts (1)",
		"Attempt 1: failed, retried, exit 1, worker -, ",
		"attempt one failed &lt;b&gt;badly&lt;/b&gt;",
		"attempt two output",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if strings.Contains(body, "<b>badly</b>") {
		t.Errorf("the attempt's stderr is not escaped")
	}
}

func TestTaskAttemptViews_Summary(t *testing.T) {
	exit := 137
	got := taskAttemptViews([]model.TaskAttempt{
		{Attempt: 1, Reason: model.AttemptReasonWorkerOffline, ExecutorType: "worker", ExternalID: "wrk_1"},
		{Attempt: 2, Reason: model.AttemptReasonWorkerRemoved, ExecutorType: "worker", ExternalID: "wrk_2", WorkerName: "node-2", ExitCode: &exit},
		{Attempt: 3, Reason: model.AttemptReasonRequeued},
		{Attempt: 4, Reason: model.AttemptReasonReset},
		// Never checked out: a queued worker task's external_id is its own id.
		{TaskID: "task_5", Attempt: 5, Reason: model.AttemptReasonRetry, ExecutorType: "worker", ExternalID: "task_5"},
		// Not a worker task: external_id is not a worker.
		{TaskID: "task_6", Attempt: 6, Reason: model.AttemptReasonReset, ExecutorType: "bvbrc", ExternalID: "job-123"},
	})
	want := []string{
		"Attempt 1: worker stopped responding, exit -, worker wrk_1, - to -",
		"Attempt 2: worker was removed, exit 137, worker node-2, - to -",
		"Attempt 3: re-queued, exit -, worker -, - to -",
		"Attempt 4: failed, re-queued by a user, exit -, worker -, - to -",
		"Attempt 5: failed, retried, exit -, worker -, - to -",
		"Attempt 6: failed, re-queued by a user, exit -, worker -, - to -",
	}
	if len(got) != len(want) {
		t.Fatalf("views = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Summary != want[i] {
			t.Errorf("summary %d = %q, want %q", i, got[i].Summary, want[i])
		}
	}
}

// Resume and recompute archive the failed attempt (reason reset) before they
// blank the task.
func TestResetFailedTaskForQueue_RecordsResetAttempt(t *testing.T) {
	tests := []struct {
		name string
		call func(u *UI, sub *model.Submission, task *model.Task) *httptest.ResponseRecorder
	}{
		{"resume", func(u *UI, sub *model.Submission, _ *model.Task) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/resume", nil)
			req.SetPathValue("id", sub.ID)
			req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
			w := httptest.NewRecorder()
			u.HandleSubmissionResume(w, req)
			return w
		}},
		{"recompute failed", func(u *UI, sub *model.Submission, _ *model.Task) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/recompute", nil)
			req.SetPathValue("id", sub.ID)
			req = withSession(req, &model.Session{ID: "s1", Username: sub.SubmittedBy, Role: "user"})
			w := httptest.NewRecorder()
			u.HandleRecomputeFailed(w, req)
			return w
		}},
		{"task recompute", func(u *UI, sub *model.Submission, task *model.Task) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/tasks/"+task.ID+"/recompute", nil)
			req.SetPathValue("id", sub.ID)
			req.SetPathValue("tid", task.ID)
			w := httptest.NewRecorder()
			u.HandleTaskRecompute(w, req)
			return w
		}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			ctx := context.Background()
			subID := "sub_att_reset_" + string(rune('a'+i))
			sub := seedRecomputeTestSubmission(t, st, subID, model.SubmissionStateFailed, testWSUser)
			task := seedFailedTask(t, st, "task_att_reset_"+string(rune('a'+i)), sub.ID, "")

			if w := tt.call(newTestUI(st), sub, task); w.Code != http.StatusOK {
				t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
			}
			checkResetAttempt(t, st, task.ID)

			live, err := st.GetTask(ctx, task.ID)
			if err != nil || live == nil || live.State != model.TaskStateQueued || live.Stderr != "" {
				t.Errorf("task = (%+v, %v), want QUEUED with blank stderr", live, err)
			}
		})
	}
}

// A reset that loses its compare-and-set (the scheduler moved the task
// FAILED→RETRYING after the handler read it) records no attempt: the archive
// and the write are one transaction.
func TestResetFailedTaskForQueue_LostCASRecordsNoAttempt(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()

	sub := seedRecomputeTestSubmission(t, st, "sub_att_lost", model.SubmissionStateFailed, testWSUser)
	task := seedFailedTask(t, st, "task_att_lost", sub.ID, "")

	u := newTestUI(&raceInjectingStore{Store: st, raceTaskID: task.ID})
	req := httptest.NewRequest(http.MethodPost, "/submissions/"+sub.ID+"/tasks/"+task.ID+"/recompute", nil)
	req.SetPathValue("id", sub.ID)
	req.SetPathValue("tid", task.ID)
	w := httptest.NewRecorder()
	u.HandleTaskRecompute(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409, body=%s", w.Code, w.Body.String())
	}

	got, err := st.ListTaskAttempts(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListTaskAttempts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("attempts = %+v, want none", got)
	}
	live, err := st.GetTask(ctx, task.ID)
	if err != nil || live == nil || live.State != model.TaskStateRetrying || live.Stderr != "stale stderr" {
		t.Errorf("task = (%+v, %v), want RETRYING with its stderr", live, err)
	}
}

func checkResetAttempt(t *testing.T, st *store.SQLiteStore, taskID string) {
	t.Helper()
	got, err := st.ListTaskAttempts(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ListTaskAttempts: %v", err)
	}
	if len(got) != 1 || got[0].Reason != model.AttemptReasonReset || got[0].Stderr != "stale stderr" ||
		got[0].ExitCode == nil || *got[0].ExitCode != 1 || got[0].State != string(model.TaskStateFailed) {
		t.Errorf("attempts = %+v, want one reset attempt with stderr \"stale stderr\", exit 1, state FAILED", got)
	}
}
