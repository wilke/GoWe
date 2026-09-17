package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// wsInputSubmissionReadyImmediately is wsInputSubmission's sibling for the
// #269 deadlock regression: its step "s1" has NO DependsOn (no "blocker"
// hack), so advanceWaiting moves it WAITING->READY on tick 1 — the very same
// tick prestageWorkspaceInputs makes its first (failing) attempt. That
// interleaving is exactly what let finalizeSubmissions's "submission running"
// transition (anyActive becomes true the moment a step is READY) race ahead
// of a still-incomplete pre-stage in production (sub_523e8a64).
func wsInputSubmissionReadyImmediately(t *testing.T, st interface {
	CreateWorkflow(ctx context.Context, wf *model.Workflow) error
	CreateSubmission(ctx context.Context, sub *model.Submission) error
	CreateStepInstance(ctx context.Context, si *model.StepInstance) error
}) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_ws_deadlock_test"
	subID := "sub_ws_deadlock_test"

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "ws-deadlock-test",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{
				ID: "s1",
				ToolInline: &model.Tool{
					ID:          "echo_tool",
					Class:       "CommandLineTool",
					BaseCommand: []string{"echo", "hello"},
				},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		UserToken:    "fake-token",
		Inputs: map[string]any{
			"reads": map[string]any{
				"class":    "File",
				"location": "ws:///user@bvbrc/home/reads.fastq",
			},
		},
		Outputs:   map[string]any{},
		Labels:    map[string]string{},
		CreatedAt: now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	si := &model.StepInstance{
		ID:           "si_ws_deadlock_test",
		SubmissionID: subID,
		StepID:       "s1",
		State:        model.StepStateWaiting,
		Outputs:      map[string]any{},
		CreatedAt:    now,
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}

	return subID
}

// TestPrestageDeadlock_NeverStrandedRunningWithoutPrestage is the #269
// regression proper.
//
// Before this commit's fix: a step with no dependencies becomes READY on
// tick 1 — the same tick prestageWorkspaceInputs makes its first (failing)
// pre-stage attempt. finalizeSubmissions then saw anyActive==true and moved
// the submission PENDING->RUNNING regardless of pre-staging being
// incomplete. From tick 2 onward, prestageWorkspaceInputs only ever
// revisited PENDING submissions, so it never saw this (now RUNNING) row
// again: the failure counter froze at 1, failPrestage's threshold was never
// reached, and dispatchReady's own pre-stage-incomplete gate deferred the
// READY step forever — a submission stuck RUNNING with zero tasks,
// indefinitely, matching production submission sub_523e8a64.
//
// Confirmed reproduced against the pre-fix code (workspace.go's
// prestageWorkspaceInputs scanning only PENDING, and loop.go's
// finalizeSubmissions activating PENDING->RUNNING unconditionally): running
// this test there fails with
//
//	state = "RUNNING", want FAILED after 12 ticks (...)
//
// i.e. the submission is left RUNNING with 0 tasks and sub.Error == nil,
// exactly the reported deadlock. After this commit's fix (RUNNING-incomplete
// submissions stay in the pre-stage scan via
// store.ListSubmissionsAwaitingPrestage, and finalizeSubmissions no longer
// activates a submission whose pre-staging is incomplete), the submission is
// never observed RUNNING while PrestageCompletedAt is nil, and instead FAILs
// with PRESTAGE_FAILED once prestageFailThreshold is reached.
func TestPrestageDeadlock_NeverStrandedRunningWithoutPrestage(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmissionReadyImmediately(t, st)

	const maxTicks = prestageFailThreshold + 2
	var final *model.Submission
	for i := 0; i < maxTicks; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}

		sub, err := st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("get submission (tick %d): %v", i+1, err)
		}
		if sub.State == model.SubmissionStateRunning && sub.PrestageCompletedAt == nil {
			t.Fatalf("tick %d: submission observed RUNNING with incomplete pre-staging "+
				"(prestage_started_at=%v, prestage_completed_at=%v) — PENDING->RUNNING must "+
				"wait for pre-staging to finish", i+1, sub.PrestageStartedAt, sub.PrestageCompletedAt)
		}
		final = sub
		if sub.State.IsTerminal() {
			break
		}
	}

	if final.State != model.SubmissionStateFailed {
		t.Fatalf("state = %q, want FAILED after %d ticks (pre-staging always fails; must give up, not deadlock RUNNING)",
			final.State, maxTicks)
	}
	if final.Error == nil || final.Error.Code != PrestageFailedCode {
		t.Fatalf("Error = %+v, want code %q", final.Error, PrestageFailedCode)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want 0 (no task may ever be created for a submission whose pre-stage never completes)", len(tasks))
	}
}
