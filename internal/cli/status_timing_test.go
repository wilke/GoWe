package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// seedTimingSubmission stores a workflow + submission + step + one SUCCESS
// task with exact hand-set timestamps and returns the submission ID.
func seedTimingSubmission(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tp := func(secs int) *time.Time { v := base.Add(time.Duration(secs) * time.Second); return &v }

	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf_timing", Name: "wf_timing", Class: "Workflow",
		Steps:     []model.Step{{ID: "step1"}},
		CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub_timing", WorkflowID: "wf_timing", WorkflowName: "wf_timing",
		State: model.SubmissionStateCompleted, Inputs: map[string]any{},
		// Anonymous CLI requests are authorized only for their own rows.
		SubmittedBy: model.AnonymousUser.Username,
		CreatedAt:   base, CompletedAt: tp(60),
	}); err != nil {
		t.Fatalf("seed submission: %v", err)
	}
	if err := st.CreateStepInstance(ctx, &model.StepInstance{
		ID: "si_1", SubmissionID: "sub_timing", StepID: "step1",
		State: model.StepStateCompleted, Outputs: map[string]any{},
		CreatedAt: base, CompletedAt: tp(50),
	}); err != nil {
		t.Fatalf("seed step instance: %v", err)
	}
	if err := st.CreateTask(ctx, &model.Task{
		ID: "task_1", SubmissionID: "sub_timing", StepID: "step1", StepInstanceID: "si_1",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal, ScatterIndex: -1,
		Tool:      map[string]any{"class": "CommandLineTool"},
		Stdout:    "SEKRIT",
		CreatedAt: base.Add(5 * time.Second), StartedAt: tp(10), CompletedAt: tp(40),
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	return "sub_timing"
}

func runStatusCmd(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	// Isolate from any real ~/.gowe/credentials.json: a stored token would
	// authenticate the request as that user, while the test rows belong to
	// the anonymous user.
	t.Setenv("HOME", t.TempDir())
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"--server", serverURL, "status"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestStatusTimingTable(t *testing.T) {
	url, st := startTestServerWithStore(t)
	subID := seedTimingSubmission(t, st)

	out, err := runStatusCmd(t, url, subID, "--timing")
	if err != nil {
		t.Fatalf("status --timing: %v (output: %s)", err, out)
	}

	for _, want := range []string{
		"Submission sub_timing [COMPLETED]",
		"wall=60.0s",
		"scheduling=5.0s",
		"compute=30.0s",
		"critical-path=45.0s", // step wall: min task created (5s) → si completed (50s)
		"STEP", "MAX-RUN",
		"step1", "45.0s",
		"TASK", "QUEUE", "RUN",
		"task_1", "5.0s", "30.0s", "SUCCESS",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SEKRIT") {
		t.Errorf("output leaks task stdout:\n%s", out)
	}
}

func TestStatusTimingJSON(t *testing.T) {
	url, st := startTestServerWithStore(t)
	subID := seedTimingSubmission(t, st)

	out, err := runStatusCmd(t, url, subID, "--timing", "--json")
	if err != nil {
		t.Fatalf("status --timing --json: %v (output: %s)", err, out)
	}

	var body struct {
		Submission map[string]any   `json:"submission"`
		Steps      []map[string]any `json:"steps"`
		Tasks      []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if body.Submission["id"] != subID {
		t.Errorf("submission.id = %v, want %s", body.Submission["id"], subID)
	}
	if len(body.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(body.Tasks))
	}
	if _, leaked := body.Tasks[0]["tool"]; leaked {
		t.Error("timing JSON leaks task tool definition")
	}
	if strings.Contains(out, "SEKRIT") {
		t.Errorf("timing JSON leaks task stdout:\n%s", out)
	}
}

func TestStatusJSONRequiresTiming(t *testing.T) {
	_, err := runStatusCmd(t, "http://127.0.0.1:1", "sub_x", "--json")
	if err == nil || !strings.Contains(err.Error(), "--json requires --timing") {
		t.Fatalf("error = %v, want --json requires --timing", err)
	}
}
