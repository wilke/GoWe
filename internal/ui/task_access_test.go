package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// The task pages check the submission named in the path first: an unknown
// submission is 404, another user's is 403, and the task must belong to it
// (404) — never a task loaded by its id alone.

const otherUIUser = "mallory@bvbrc"

func otherUISession() *model.Session {
	return &model.Session{ID: "s-other", Username: otherUIUser, Role: "user"}
}

func adminUISession() *model.Session {
	return &model.Session{ID: "s-admin", Username: "admin", Role: string(model.RoleAdmin)}
}

// uiTaskReq builds a request for /submissions/{subID}/tasks/{taskID}{suffix}
// as sess.
func uiTaskReq(method, subID, taskID, suffix string, sess *model.Session) *http.Request {
	req := httptest.NewRequest(method, "/submissions/"+subID+"/tasks/"+taskID+suffix, nil)
	req.SetPathValue("id", subID)
	req.SetPathValue("tid", taskID)
	return withSession(req, sess)
}

func TestHandleTaskLogs_SubmissionChecked(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	owner := seedRecomputeTestSubmission(t, st, "sub_acc_owner", model.SubmissionStateFailed, testWSUser)
	other := seedRecomputeTestSubmission(t, st, "sub_acc_other", model.SubmissionStateFailed, otherUIUser)
	task := seedFailedTask(t, st, "task_acc_owner", owner.ID, "") // stderr "stale stderr"
	u := newTestUI(st)

	tests := []struct {
		name string
		sub  string
		sess *model.Session
		want int
	}{
		{"unknown submission, another user", "sub_does_not_exist", otherUISession(), http.StatusNotFound},
		{"own submission, another user's task", other.ID, otherUISession(), http.StatusNotFound},
		{"admin, wrong submission", other.ID, adminUISession(), http.StatusNotFound},
		{"the task's submission, another user", owner.ID, otherUISession(), http.StatusForbidden},
		{"owner", owner.ID, testSession(), http.StatusOK},
		{"admin", owner.ID, adminUISession(), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			u.HandleTaskLogs(w, uiTaskReq(http.MethodGet, tt.sub, task.ID, "/logs", tt.sess))
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.want, w.Body.String())
			}
			served := strings.Contains(w.Body.String(), "stale stderr")
			if tt.want == http.StatusOK && !served {
				t.Errorf("the owner's page lacks the task's stderr")
			}
			if tt.want != http.StatusOK && served {
				t.Errorf("the task's stderr was served: %s", w.Body.String())
			}
		})
	}
}

func TestHandleTaskRecompute_SubmissionChecked(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()
	owner := seedRecomputeTestSubmission(t, st, "sub_rc_owner", model.SubmissionStateFailed, testWSUser)
	other := seedRecomputeTestSubmission(t, st, "sub_rc_other", model.SubmissionStateFailed, otherUIUser)
	failed := seedFailedTask(t, st, "task_rc_failed", owner.ID, "")
	done := &model.Task{
		ID: "task_rc_done", SubmissionID: owner.ID, StepID: "step2",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal, CreatedAt: uiAt(0),
	}
	seedSWTask(t, st, done)
	u := newTestUI(st)

	tests := []struct {
		name   string
		sub    string
		taskID string
		want   int
	}{
		{"unknown submission", "sub_does_not_exist", failed.ID, http.StatusNotFound},
		{"own submission, another user's task", other.ID, failed.ID, http.StatusNotFound},
		{"another user's submission, FAILED task", owner.ID, failed.ID, http.StatusForbidden},
		// Not 400: the task's state is not revealed to a non-owner.
		{"another user's submission, SUCCESS task", owner.ID, done.ID, http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			u.HandleTaskRecompute(w, uiTaskReq(http.MethodPost, tt.sub, tt.taskID, "/recompute", otherUISession()))
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.want, w.Body.String())
			}
		})
	}
	if got, err := st.GetTask(ctx, failed.ID); err != nil || got == nil || got.State != model.TaskStateFailed {
		t.Errorf("task = (%+v, %v), want it left FAILED", got, err)
	}
}

func TestHandleSubmissionTaskChildren_TaskOutsideTree(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	_, _, midProxy := buildSubworkflowTree(t, st, testWSUser) // root sub_root of testWSUser
	seedSWSubmission(t, st, &model.Submission{
		ID: "sub_other_root", WorkflowName: "other-wf", State: model.SubmissionStateRunning,
		SubmittedBy: otherUIUser, CreatedAt: uiAt(0),
	})
	u := newSubworkflowTestUI(st)

	tests := []struct {
		name string
		root string
		sess *model.Session
		want int
	}{
		{"unknown root submission", "sub_does_not_exist", otherUISession(), http.StatusNotFound},
		{"own root, another user's proxy task", "sub_other_root", otherUISession(), http.StatusNotFound},
		{"admin, task outside the root's tree", "sub_other_root", adminUISession(), http.StatusNotFound},
		{"another user's root", "sub_root", otherUISession(), http.StatusForbidden},
		{"owner", "sub_root", testSession(), http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			u.HandleSubmissionTaskChildren(w, uiTaskReq(http.MethodGet, tt.root, midProxy.ID, "/children", tt.sess))
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.want, w.Body.String())
			}
			if tt.want != http.StatusOK && strings.Contains(w.Body.String(), "leaf-a") {
				t.Errorf("the child submission's tasks were served: %s", w.Body.String())
			}
		})
	}
}
