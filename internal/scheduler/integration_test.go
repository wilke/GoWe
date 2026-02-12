package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// TestIntegration_TwoStepLocalPipeline verifies the full scheduler lifecycle:
// create workflow -> create submission + tasks -> run Tick() until COMPLETED.
//
// The pipeline has two steps:
//   - step1: no dependencies, runs "echo hello from step1"
//   - step2: depends on step1, runs "echo hello from step2"
func TestIntegration_TwoStepLocalPipeline(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// --- Store ---
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// --- Executor ---
	reg := executor.NewRegistry(logger)
	reg.Register(executor.NewLocalExecutor(t.TempDir(), logger))

	// --- Scheduler ---
	sched := NewLoop(st, reg, DefaultConfig(), logger)

	// --- Workflow ---
	wf := &model.Workflow{
		ID:         "wf_" + uuid.New().String(),
		Name:       "test-pipeline",
		CWLVersion: "v1.2",
		RawCWL:     "test",
		Steps: []model.Step{
			{
				ID:      "step1",
				ToolRef: "echo-tool-1",
				ToolInline: &model.Tool{
					ID:          "echo-tool-1",
					Class:       "CommandLineTool",
					BaseCommand: []string{"echo", "hello from step1"},
					Inputs:      []model.ToolInput{{ID: "dummy", Type: "string"}},
					Outputs:     []model.ToolOutput{},
				},
				DependsOn: []string{},
				In:        []model.StepInput{},
				Out:       []string{},
			},
			{
				ID:      "step2",
				ToolRef: "echo-tool-2",
				ToolInline: &model.Tool{
					ID:          "echo-tool-2",
					Class:       "CommandLineTool",
					BaseCommand: []string{"echo", "hello from step2"},
					Inputs:      []model.ToolInput{{ID: "dummy", Type: "string"}},
					Outputs:     []model.ToolOutput{},
				},
				DependsOn: []string{"step1"},
				In:        []model.StepInput{},
				Out:       []string{},
			},
		},
		Inputs:    []model.WorkflowInput{},
		Outputs:   []model.WorkflowOutput{},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	// --- Submission ---
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		CreatedAt:    time.Now().UTC(),
	}

	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	// --- Tasks ---
	task1 := &model.Task{
		ID:           "task_" + uuid.New().String(),
		SubmissionID: sub.ID,
		StepID:       "step1",
		State:        model.TaskStatePending,
		ExecutorType: model.ExecutorTypeLocal,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		DependsOn:    []string{},
		MaxRetries:   0,
		CreatedAt:    time.Now().UTC(),
	}

	task2 := &model.Task{
		ID:           "task_" + uuid.New().String(),
		SubmissionID: sub.ID,
		StepID:       "step2",
		State:        model.TaskStatePending,
		ExecutorType: model.ExecutorTypeLocal,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		DependsOn:    []string{"step1"},
		MaxRetries:   0,
		CreatedAt:    time.Now().UTC(),
	}

	if err := st.CreateTask(ctx, task1); err != nil {
		t.Fatalf("create task1: %v", err)
	}
	if err := st.CreateTask(ctx, task2); err != nil {
		t.Fatalf("create task2: %v", err)
	}

	// --- Run Tick loop ---
	const maxTicks = 10
	for tick := 1; tick <= maxTicks; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d error: %v", tick, err)
		}

		// Reload submission to check state.
		got, err := st.GetSubmission(ctx, sub.ID)
		if err != nil {
			t.Fatalf("tick %d: get submission: %v", tick, err)
		}

		if got.State == model.SubmissionStateCompleted {
			t.Logf("submission completed after %d tick(s)", tick)

			// Verify both tasks reached SUCCESS.
			for _, task := range got.Tasks {
				if task.State != model.TaskStateSuccess {
					t.Errorf("task %s (step %s): want state SUCCESS, got %s", task.ID, task.StepID, task.State)
				}

				if task.ExitCode == nil {
					t.Errorf("task %s (step %s): exit code is nil, want 0", task.ID, task.StepID)
				} else if *task.ExitCode != 0 {
					t.Errorf("task %s (step %s): exit code = %d, want 0", task.ID, task.StepID, *task.ExitCode)
				}
			}

			// Verify stdout contains expected output per step.
			tasksByStep := make(map[string]model.Task, len(got.Tasks))
			for _, task := range got.Tasks {
				tasksByStep[task.StepID] = task
			}

			if t1, ok := tasksByStep["step1"]; !ok {
				t.Error("task for step1 not found in submission")
			} else if !strings.Contains(t1.Stdout, "hello from step1") {
				t.Errorf("step1 stdout = %q, want it to contain %q", t1.Stdout, "hello from step1")
			}

			if t2, ok := tasksByStep["step2"]; !ok {
				t.Error("task for step2 not found in submission")
			} else if !strings.Contains(t2.Stdout, "hello from step2") {
				t.Errorf("step2 stdout = %q, want it to contain %q", t2.Stdout, "hello from step2")
			}

			return // success
		}
	}

	t.Fatalf("submission did not reach COMPLETED after %d ticks", maxTicks)
}
