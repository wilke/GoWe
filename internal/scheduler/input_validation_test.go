package scheduler

import (
	"context"
	"testing"

	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/model"
)

// enumToolValueFromCWL is a one-step workflow whose step feeds a literal,
// intentionally-invalid string into a CommandLineTool's enum input via
// valueFrom (a plain literal, no "$(" — see cwlexpr.Evaluate — so no
// InlineJavascriptRequirement is needed). Used to exercise #273's
// tool-level validation, which the workflow-level submission check never
// sees (the workflow itself declares no inputs).
const enumToolValueFromCWL = `{
  "$graph": [
    {
      "id": "#main",
      "class": "Workflow",
      "inputs": [],
      "outputs": [{"id": "out", "type": "File", "outputSource": "step/out"}],
      "steps": {
        "step": {
          "run": "#tool",
          "in": {"chunk_method": {"valueFrom": "not-a-symbol"}},
          "out": ["out"]
        }
      }
    },
    {
      "id": "#tool",
      "class": "CommandLineTool",
      "baseCommand": ["echo", "hello"],
      "inputs": [
        {"id": "chunk_method", "type": {"type": "enum", "symbols": ["fixed", "semantic"]}}
      ],
      "outputs": [{"id": "out", "type": "stdout"}],
      "stdout": "output.txt"
    }
  ]
}`

// enumToolValueFromSteps builds the model.Step slice matching
// enumToolValueFromCWL's single step, wired with the same valueFrom (the
// model.Step used for scheduling is hand-built here, independent of the raw
// CWL text parsed for the tool definition — see populateToolAndJob).
func enumToolValueFromSteps() []model.Step {
	return []model.Step{
		{
			ID:      "step",
			ToolRef: "tool",
			ToolInline: &model.Tool{
				ID:          "tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
			In:  []model.StepInput{{ID: "chunk_method", ValueFrom: "not-a-symbol"}},
			Out: []string{"out"},
		},
	}
}

// TestPopulateToolAndJob_StampsInputValidationMode covers the #273 stamping
// contract: populateToolAndJob writes the Loop's own input-validation mode
// into the task's RuntimeHints.InputValidation, so a worker (or, indirectly
// through createTaskFromStep's shared RuntimeHints pointer, every scatter
// combination) inherits the server's policy without its own flag.
func TestPopulateToolAndJob_StampsInputValidationMode(t *testing.T) {
	tests := []struct {
		name string
		mode validate.Mode
		want string
	}{
		{name: "enforce", mode: validate.ModeEnforce, want: "enforce"},
		{name: "warn", mode: validate.ModeWarn, want: "warn"},
		{name: "off", mode: validate.ModeOff, want: "off"},
		{name: "unset (zero value)", mode: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, _ := testSetup(t)
			sched.SetInputValidation(tt.mode)

			wf := &model.Workflow{ID: "wf1", RawCWL: enumToolValueFromCWL}
			steps := enumToolValueFromSteps()
			step := &steps[0]
			task := &model.Task{}

			if err := sched.populateToolAndJob(task, step, wf, map[string]any{}, map[string]*model.Task{}); err != nil {
				t.Fatalf("populateToolAndJob: %v", err)
			}
			if task.RuntimeHints == nil {
				t.Fatal("task.RuntimeHints is nil, want it stamped with InputValidation")
			}
			if task.RuntimeHints.InputValidation != tt.want {
				t.Errorf("RuntimeHints.InputValidation = %q, want %q", task.RuntimeHints.InputValidation, tt.want)
			}
		})
	}
}

// TestToolLevelInputValidation_Enforce_FailsOnceNoRetries: a step whose
// valueFrom feeds an out-of-range enum value to its tool fails at the local
// executor (cwltool.ExecuteTool -> validate.ToolInputs, enforce mode) before
// the command ever runs. Per #273 item 6, this must be treated as
// non-retryable: the task reaches FAILED after its first (and only)
// attempt, with RetryCount left at 0 despite MaxRetries being configured
// high enough that a normal failure would retry.
func TestToolLevelInputValidation_Enforce_FailsOnceNoRetries(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 3
	sched.SetInputValidation(validate.ModeEnforce)

	_, subID := createPipeline(t, st, enumToolValueFromSteps(), map[string]any{}, 0)
	ctx := context.Background()
	sub, _ := st.GetSubmission(ctx, subID)
	wf, _ := st.GetWorkflow(ctx, sub.WorkflowID)
	wf.RawCWL = enumToolValueFromCWL
	if err := st.UpdateWorkflow(ctx, wf); err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}

	final := runToTerminal(t, sched, st, subID, 10)
	if final.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %q, want FAILED", final.State)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want exactly 1 (no retry attempts)", len(tasks))
	}
	task := tasks[0]
	if task.State != model.TaskStateFailed {
		t.Errorf("task.State = %q, want FAILED", task.State)
	}
	if task.RetryCount != 0 {
		t.Errorf("task.RetryCount = %d, want 0 (permanent failure, no retries consumed)", task.RetryCount)
	}
	if task.MaxRetries != task.RetryCount {
		t.Errorf("task.MaxRetries = %d, task.RetryCount = %d, want them equal (the terminal idiom)", task.MaxRetries, task.RetryCount)
	}
}

// TestToolLevelInputValidation_Warn_RunsAnyway: the same bad-enum step
// completes successfully (the tool never actually reads chunk_method,
// since it has no inputBinding) when the server runs in warn mode — the
// #273 type check only logs, it never blocks tool-level execution.
func TestToolLevelInputValidation_Warn_RunsAnyway(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 3
	sched.SetInputValidation(validate.ModeWarn)

	_, subID := createPipeline(t, st, enumToolValueFromSteps(), map[string]any{}, 0)
	ctx := context.Background()
	sub, _ := st.GetSubmission(ctx, subID)
	wf, _ := st.GetWorkflow(ctx, sub.WorkflowID)
	wf.RawCWL = enumToolValueFromCWL
	if err := st.UpdateWorkflow(ctx, wf); err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}

	final := runToTerminal(t, sched, st, subID, 10)
	if final.State != model.SubmissionStateCompleted {
		t.Fatalf("submission state = %q, want COMPLETED, error=%v", final.State, final.Error)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want exactly 1", len(tasks))
	}
	if tasks[0].State != model.TaskStateSuccess {
		t.Errorf("task.State = %q, want SUCCESS", tasks[0].State)
	}
}
