package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

var uiBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func uiAt(secs int) time.Time   { return uiBase.Add(time.Duration(secs) * time.Second) }
func uiAtp(secs int) *time.Time { t := uiAt(secs); return &t }

// TestTaskQueueDisplay exercises the timing trust rules behind the Queue
// column: started_at is trusted only in RUNNING/SUCCESS/FAILED, never on a
// QUEUED worker task (stale dispatch stamp).
func TestTaskQueueDisplay(t *testing.T) {
	fn, ok := templateFuncs["taskQueueDisplay"].(func(model.Task) string)
	if !ok {
		t.Fatal("taskQueueDisplay func missing or wrong signature")
	}

	tests := []struct {
		name string
		task model.Task
		want string // exact match, or "waiting" prefix when wantPrefix
	}{
		{
			name: "success trusted window",
			task: model.Task{State: model.TaskStateSuccess, CreatedAt: uiAt(0),
				StartedAt: uiAtp(10), CompletedAt: uiAtp(40)},
			want: "10s",
		},
		{
			name: "running trusted window",
			task: model.Task{State: model.TaskStateRunning, CreatedAt: uiAt(0), StartedAt: uiAtp(5)},
			want: "5s",
		},
		{
			name: "queued worker ignores stale started_at",
			task: model.Task{State: model.TaskStateQueued, ExecutorType: model.ExecutorTypeWorker,
				CreatedAt: time.Now().UTC().Add(-30 * time.Second), StartedAt: uiAtp(1)},
			want: "waiting",
		},
		{
			name: "pending waits",
			task: model.Task{State: model.TaskStatePending, CreatedAt: time.Now().UTC()},
			want: "waiting",
		},
		{
			name: "skipped never started shows wait until cancel",
			task: model.Task{State: model.TaskStateSkipped, CreatedAt: uiAt(0), CompletedAt: uiAtp(20)},
			want: "20s",
		},
		{
			name: "skipped after start shows nothing",
			task: model.Task{State: model.TaskStateSkipped, CreatedAt: uiAt(0),
				StartedAt: uiAtp(5), CompletedAt: uiAtp(20)},
			want: "—",
		},
		{
			name: "when-skip synthetic shows nothing",
			task: model.Task{State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeSubworkflow,
				CreatedAt: uiAt(0), CompletedAt: uiAtp(0)},
			want: "—",
		},
		{
			name: "failed before start shows whole wait",
			task: model.Task{State: model.TaskStateFailed, CreatedAt: uiAt(0), CompletedAt: uiAtp(15)},
			want: "15s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fn(tt.task)
			if tt.want == "waiting" {
				if !strings.HasPrefix(got, "waiting ") {
					t.Errorf("got %q, want waiting prefix", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTaskRunDisplay: run duration only for trusted windows.
func TestTaskRunDisplay(t *testing.T) {
	fn, ok := templateFuncs["taskRunDisplay"].(func(model.Task) string)
	if !ok {
		t.Fatal("taskRunDisplay func missing or wrong signature")
	}

	tests := []struct {
		name string
		task model.Task
		want string
	}{
		{
			name: "success",
			task: model.Task{State: model.TaskStateSuccess, CreatedAt: uiAt(0),
				StartedAt: uiAtp(10), CompletedAt: uiAtp(40)},
			want: "30s",
		},
		{
			name: "failed",
			task: model.Task{State: model.TaskStateFailed, CreatedAt: uiAt(0),
				StartedAt: uiAtp(10), CompletedAt: uiAtp(25)},
			want: "15s",
		},
		{
			name: "queued worker with stale stamp shows dash",
			task: model.Task{State: model.TaskStateQueued, ExecutorType: model.ExecutorTypeWorker,
				CreatedAt: uiAt(0), StartedAt: uiAtp(1)},
			want: "—",
		},
		{
			name: "running shows dash until complete",
			task: model.Task{State: model.TaskStateRunning, CreatedAt: uiAt(0), StartedAt: uiAtp(5)},
			want: "—",
		},
		{
			name: "retrying shows last attempt window",
			task: model.Task{State: model.TaskStateRetrying, CreatedAt: uiAt(0),
				StartedAt: uiAtp(10), CompletedAt: uiAtp(20)},
			want: "10s (retrying)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fn(tt.task); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSubmissionDetailTemplateRendersTimingColumns is the package's first
// template render test: the submission detail page must render and include
// the Queue/Run columns with trust-rule-derived values.
func TestSubmissionDetailTemplateRendersTimingColumns(t *testing.T) {
	sub := &model.Submission{
		ID: "sub_ui", WorkflowID: "wf_ui", WorkflowName: "wf_ui",
		State:     model.SubmissionStateRunning,
		CreatedAt: uiAt(0),
		Tasks: []model.Task{
			{
				ID: "task_done", SubmissionID: "sub_ui", StepID: "step1",
				State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal,
				CreatedAt: uiAt(0), StartedAt: uiAtp(10), CompletedAt: uiAtp(40),
			},
			{
				ID: "task_queued", SubmissionID: "sub_ui", StepID: "step2",
				State: model.TaskStateQueued, ExecutorType: model.ExecutorTypeWorker,
				CreatedAt: time.Now().UTC().Add(-30 * time.Second), StartedAt: uiAtp(1),
			},
		},
	}
	sub.TaskSummary = model.ComputeTaskSummary(sub.Tasks)

	var buf strings.Builder
	err := renderTemplate(&buf, "submissions/detail", map[string]any{
		"Title":      "Submission sub_ui - GoWe",
		"Session":    nil,
		"Submission": sub,
		"Workflow":   nil,
	})
	if err != nil {
		t.Fatalf("render submissions/detail: %v", err)
	}
	out := buf.String()

	for _, want := range []string{">Queue<", ">Run<", "30s", "waiting "} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}
