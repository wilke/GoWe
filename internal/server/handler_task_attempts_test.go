package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// createSubmissionAs creates a submission of wfID as the user of token and
// returns its id.
func createSubmissionAs(t *testing.T, srv *Server, wfID, token string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(body), token)
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	id, _ := data["id"].(string)
	if id == "" {
		t.Fatalf("created submission has no id: %s", string(env.Data))
	}
	return id
}

// seedFailedTask creates a FAILED task of subID with output and an exit code.
func seedFailedTask(t *testing.T, st store.Store, subID, id string) {
	t.Helper()
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	exit := 1
	tk := &model.Task{
		ID: id, SubmissionID: subID, StepID: "s", State: model.TaskStateFailed,
		ExecutorType: model.ExecutorTypeLocal, Inputs: map[string]any{}, Outputs: map[string]any{},
		DependsOn: []string{}, MaxRetries: 3, Stderr: "boom from " + id, ExitCode: &exit,
		CreatedAt: now, StartedAt: &started, CompletedAt: &now,
	}
	if err := st.CreateTask(context.Background(), tk); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
}

func TestListTaskAttempts_Route(t *testing.T) {
	ctx := context.Background()
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	srv, st := testServerWithStore(WithAdminConfig(adminCfg))

	wfID := createTestWorkflow(t, srv)
	subA := createSubmissionAs(t, srv, wfID, userAuthToken)
	subB := createSubmissionAs(t, srv, wfID, userAuthToken)
	seedFailedTask(t, st, subA, "task_route_a")
	seedFailedTask(t, st, subB, "task_route_b")
	// Archive task_route_a's failed attempt; the row itself is written back
	// unchanged.
	tkA, err := st.GetTask(ctx, "task_route_a")
	if err != nil || tkA == nil {
		t.Fatalf("GetTask = (%v, %v)", tkA, err)
	}
	if applied, err := st.ArchiveAndResetTask(ctx, tkA, model.TaskStateFailed, model.AttemptReasonRetry); err != nil || !applied {
		t.Fatalf("ArchiveAndResetTask = (%v, %v), want (true, nil)", applied, err)
	}

	pathA := "/api/v1/submissions/" + subA + "/tasks/task_route_a/attempts"
	tests := []struct {
		name  string
		path  string
		token string
		want  int
	}{
		{"owner", pathA, userAuthToken, http.StatusOK},
		{"admin", pathA, adminAuthToken, http.StatusOK},
		{"another user", pathA, plainToken, http.StatusForbidden},
		{"task of another submission", "/api/v1/submissions/" + subA + "/tasks/task_route_b/attempts", userAuthToken, http.StatusNotFound},
		{"task of another submission, admin", "/api/v1/submissions/" + subA + "/tasks/task_route_b/attempts", adminAuthToken, http.StatusNotFound},
		{"unknown task", "/api/v1/submissions/" + subA + "/tasks/task_nope/attempts", userAuthToken, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, env := doGetAs(t, srv, tt.path, tt.token)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.want, w.Body.String())
			}
			if tt.want != http.StatusOK {
				return
			}
			var data struct {
				Attempts []model.TaskAttempt `json:"attempts"`
			}
			if err := json.Unmarshal(env.Data, &data); err != nil {
				t.Fatalf("decode: %v (data=%s)", err, string(env.Data))
			}
			if len(data.Attempts) != 1 {
				t.Fatalf("attempts = %+v, want 1", data.Attempts)
			}
			a := data.Attempts[0]
			if a.Attempt != 1 || a.Reason != model.AttemptReasonRetry || a.Stderr != "boom from task_route_a" ||
				a.ExitCode == nil || *a.ExitCode != 1 || a.TaskID != "task_route_a" {
				t.Errorf("attempt = %+v, want #1 retry with the task's stderr and exit 1", a)
			}
		})
	}

	// A task without earlier attempts: an empty list, not null.
	w, env := doGetAs(t, srv, "/api/v1/submissions/"+subB+"/tasks/task_route_b/attempts", userAuthToken)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil || string(raw["attempts"]) != "[]" {
		t.Errorf("data = %s (%v), want {\"attempts\":[]}", string(env.Data), err)
	}
}
