package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// gowe status prints one line under a service task that has a status, with
// the state the server shows (stopped for an ended task); nothing under a
// task without one.
func TestStatusServiceLine(t *testing.T) {
	url, st := startTestServerWithStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf_svc", Name: "svc", Class: "Workflow", Steps: []model.Step{{ID: "serve"}},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub_svc", WorkflowID: "wf_svc", WorkflowName: "svc", State: model.SubmissionStateRunning,
		Inputs: map[string]any{}, SubmittedBy: model.AnonymousUser.Username, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	readyAt := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	for _, tk := range []struct {
		id, step string
		state    model.TaskState
		status   *model.ServiceStatus
	}{
		{"task_svc", "serve", model.TaskStateRunning, &model.ServiceStatus{State: model.ServiceStateReady, Host: "gpu01", Port: 8000,
			Scheme: "https", GPUs: []model.ServiceGPU{{Index: 2}}, ReadyAt: &readyAt, UpdatedAt: now}},
		{"task_old", "old", model.TaskStateFailed, &model.ServiceStatus{State: model.ServiceStateReady, Host: "gpu02", Port: 9000, UpdatedAt: now}},
		{"task_none", "plain", model.TaskStateRunning, nil},
	} {
		task := &model.Task{
			ID: tk.id, SubmissionID: "sub_svc", StepID: tk.step, State: tk.state,
			ExecutorType: model.ExecutorTypeWorker, ExternalID: "wrk_svc", StartedAt: &now,
			Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
			RuntimeHints:  &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}},
			ServiceStatus: tk.status,
		}
		if tk.state.IsTerminal() {
			task.CompletedAt = &now
		}
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", tk.id, err)
		}
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdOut, runErr := runStatusCmd(t, url, "sub_svc")
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()
	if runErr != nil {
		t.Fatalf("status: %v (%s%s)", runErr, cmdOut, out)
	}

	for _, want := range []string{
		"    - serve: RUNNING\n      service: ready  https://gpu01:8000  GPUs 2  ready since 2026-11-27T10:00:00Z\n",
		"    - old: FAILED\n      service: stopped  http://gpu02:9000\n",
		"    - plain: RUNNING\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "service: "); n != 2 {
		t.Errorf("service lines = %d, want 2:\n%s", n, out)
	}
	if strings.Contains(out, "last reported") {
		t.Errorf("a fresh status is marked stale:\n%s", out)
	}
}

// serviceStatusLine judges staleness against the now it is given: a reported
// state last updated more than 2 minutes before it gets "(last reported ...)";
// a fresh one and the server-shown states (stopped, lost) do not.
func TestServiceStatusLine_Stale(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	field := func(state string, updated time.Time) any {
		return map[string]any{"state": state, "host": "gpu01", "port": 8000, "updated_at": updated.Format(time.RFC3339Nano)}
	}
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"fresh", field(model.ServiceStateReady, now.Add(-30*time.Second)), "ready  http://gpu01:8000"},
		{"stale", field(model.ServiceStateReady, now.Add(-5*time.Minute)), "ready  http://gpu01:8000 (last reported 2026-11-27T11:55:00Z)"},
		{"stale, stopping", field(model.ServiceStateStopping, now.Add(-5*time.Minute)), "stopping  http://gpu01:8000 (last reported 2026-11-27T11:55:00Z)"},
		{"stopped", field(model.ServiceStateStopped, now.Add(-time.Hour)), "stopped  http://gpu01:8000"},
		{"lost", field(model.ServiceStateLost, now.Add(-time.Hour)), "lost  http://gpu01:8000"},
		{"absent", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceStatusLine(tt.v, now); got != tt.want {
				t.Errorf("serviceStatusLine = %q, want %q", got, tt.want)
			}
		})
	}
}
