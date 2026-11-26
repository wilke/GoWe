package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The task read endpoints check the submission named in the path: it must
// exist (404), be the caller's or the caller an admin (403), and the task
// must belong to it (404). Before, an unknown submission id skipped the
// access check and the task was loaded by its id alone, so any caller who
// knew a task id (shown in /api/v1/fleet) could read another user's task and
// its logs.
func TestTaskRead_SubmissionChecked(t *testing.T) {
	ctx := context.Background()
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	srv, st := testServerWithStore(WithAdminConfig(adminCfg))

	wfID := createTestWorkflow(t, srv)
	ownerSub := createSubmissionAs(t, srv, wfID, userAuthToken) // user "tester"
	otherSub := createSubmissionAs(t, srv, wfID, plainToken)    // user "plain"
	const taskID = "task_access_owner"
	seedFailedTask(t, st, ownerSub, taskID) // stderr "boom from task_access_owner"
	if tk, err := st.GetTask(ctx, taskID); err != nil || tk == nil {
		t.Fatalf("GetTask = (%v, %v)", tk, err)
	}
	const secretOut = "boom from " + taskID

	tasksOf := func(subID string) string { return "/api/v1/submissions/" + subID + "/tasks/" }
	tests := []struct {
		name  string
		sub   string
		token string
		want  int
	}{
		{"unknown submission, another user", "sub_does_not_exist", plainToken, http.StatusNotFound},
		{"unknown submission, admin", "sub_does_not_exist", adminAuthToken, http.StatusNotFound},
		{"own submission, another user's task", otherSub, plainToken, http.StatusNotFound},
		{"another user's submission, admin, wrong task", otherSub, adminAuthToken, http.StatusNotFound},
		{"the task's submission, another user", ownerSub, plainToken, http.StatusForbidden},
		{"owner", ownerSub, userAuthToken, http.StatusOK},
		{"admin", ownerSub, adminAuthToken, http.StatusOK},
	}
	for _, tt := range tests {
		for _, suffix := range []string{"", "/logs"} {
			path := tasksOf(tt.sub) + taskID + suffix
			t.Run(tt.name+suffix, func(t *testing.T) {
				w, _ := doGetAs(t, srv, path, tt.token)
				if w.Code != tt.want {
					t.Fatalf("GET %s: status = %d, want %d, body=%s", path, w.Code, tt.want, w.Body.String())
				}
				body := w.Body.String()
				if tt.want != http.StatusOK {
					if strings.Contains(body, secretOut) {
						t.Errorf("GET %s: the task's output was served: %s", path, body)
					}
					return
				}
				// The task record carries no stderr (json:"-"); the logs do.
				want := `"id":"` + taskID + `"`
				if suffix == "/logs" {
					want = secretOut
				}
				if !strings.Contains(body, want) {
					t.Errorf("GET %s: body lacks %q: %s", path, want, body)
				}
			})
		}
	}

	// The task list of an unknown submission is 404 too (it used to skip the
	// access check and answer an empty list).
	if w, _ := doGetAs(t, srv, tasksOf("sub_does_not_exist"), plainToken); w.Code != http.StatusNotFound {
		t.Errorf("list tasks of an unknown submission: %d, want 404", w.Code)
	}
}
