package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// This file covers GoWe issue #128: the scheduler hot-looped on step
// instances orphaned by a terminal (COMPLETED/FAILED/CANCELLED) submission —
// especially once the submission's workflow had been deleted — re-selecting
// them, re-failing the "get workflow" lookup, and logging an error every tick
// forever (production evidence: 304 identical log lines for a single
// submission, 0 forward progress).
//
// Two independent defects, two independent fixes:
//
//  1. advanceWaiting/dispatchReady/advanceSteps now skip (and terminalize)
//     any step instance whose owning submission is already terminal —
//     orphanedByTerminalSubmission SKIPs it once with a diagnostic Error, so
//     it leaves the WAITING/READY/DISPATCHED/RUNNING working set permanently.
//  2. When a workflow can't be loaded for a submission that is NOT yet
//     terminal, getWorkflowOrFail bounds the retry: after
//     missingWorkflowFailThreshold consecutive ticks, failSubmissionMissingWorkflow
//     FAILs the submission with a persisted WORKFLOW_UNAVAILABLE error instead
//     of looping forever.

// TestOrphanedStepInstances_TerminalSubmission_Terminalized covers defect 1:
// WAITING, DISPATCHED, and RUNNING step instances belonging to an already
// CANCELLED submission (simulating a cancel whose cascade missed them, per
// the production evidence in #128) must be SKIPPED with a diagnostic Error on
// the very first tick that sees them, and must never be touched again by
// subsequent ticks (proven by CompletedAt staying fixed — a repeat
// terminalization would move it forward).
func TestOrphanedStepInstances_TerminalSubmission_Terminalized(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_" + uuid.New().String()
	subID := "sub_" + uuid.New().String()

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "orphan-test-workflow",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{ID: "s_waiting", ToolInline: &model.Tool{ID: "t1", Class: "CommandLineTool", BaseCommand: []string{"echo"}}},
			{ID: "s_dispatched", ToolInline: &model.Tool{ID: "t2", Class: "CommandLineTool", BaseCommand: []string{"echo"}}},
			{ID: "s_running", ToolInline: &model.Tool{ID: "t3", Class: "CommandLineTool", BaseCommand: []string{"echo"}}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	// Submission is already CANCELLED — the workflow it references still
	// exists, isolating this test from defect 2 (missing workflow).
	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStateCancelled,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	stepStates := map[string]model.StepInstanceState{
		"s_waiting":    model.StepStateWaiting,
		"s_dispatched": model.StepStateDispatched,
		"s_running":    model.StepStateRunning,
	}
	for stepID, state := range stepStates {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: subID,
			StepID:       stepID,
			State:        state,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", stepID, err)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	byStep := getStepInstancesByStep(t, st, subID)
	firstCompletedAt := make(map[string]time.Time, len(stepStates))
	for stepID := range stepStates {
		si := byStep[stepID]
		if si.State != model.StepStateSkipped {
			t.Errorf("step %s state = %s, want SKIPPED", stepID, si.State)
		}
		if si.CompletedAt == nil {
			t.Fatalf("step %s CompletedAt is nil, want set", stepID)
		}
		if !strings.Contains(si.Error, "orphaned by terminal submission") {
			t.Errorf("step %s Error = %q, want it to contain %q", stepID, si.Error, "orphaned by terminal submission")
		}
		if !strings.Contains(si.Error, subID) {
			t.Errorf("step %s Error = %q, want it to mention the submission id %q", stepID, si.Error, subID)
		}
		firstCompletedAt[stepID] = *si.CompletedAt
	}

	// Further ticks must not re-select or re-process these step instances:
	// they're terminal now, so ListStepsByState(WAITING/DISPATCHED/RUNNING)
	// no longer returns them, and CompletedAt must not move.
	for i := 0; i < 3; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("extra tick %d: %v", i+2, err)
		}
	}

	byStep = getStepInstancesByStep(t, st, subID)
	for stepID := range stepStates {
		si := byStep[stepID]
		if si.State != model.StepStateSkipped {
			t.Errorf("step %s state after extra ticks = %s, want still SKIPPED", stepID, si.State)
		}
		if si.CompletedAt == nil || !si.CompletedAt.Equal(firstCompletedAt[stepID]) {
			t.Errorf("step %s CompletedAt changed across ticks (reprocessed): got %v, want unchanged %v",
				stepID, si.CompletedAt, firstCompletedAt[stepID])
		}
	}

	gotSub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if gotSub.State != model.SubmissionStateCancelled {
		t.Errorf("submission state = %s, want still CANCELLED (untouched)", gotSub.State)
	}
}

// TestMissingWorkflow_ActiveSubmission_FailsAfterThreshold covers defect 2:
// a non-terminal (PENDING) submission whose workflow has been deleted must
// not loop the "get workflow" retry forever. It stays PENDING for the first
// missingWorkflowFailThreshold-1 ticks (bounded retry in progress), then FAILs
// on the threshold-th tick with a persisted, descriptive error. A following
// tick then terminalizes the leftover step instance via the terminal-submission
// guard from defect 1 — proving the two fixes compose.
func TestMissingWorkflow_ActiveSubmission_FailsAfterThreshold(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	wfID, subID := createPipeline(t, st, []model.Step{
		{ID: "s1", ToolInline: &model.Tool{ID: "t1", Class: "CommandLineTool", BaseCommand: []string{"echo"}}},
	}, map[string]any{}, 0)

	if err := st.DeleteWorkflow(ctx, wfID); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}

	for i := 1; i < missingWorkflowFailThreshold; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		sub, err := st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("GetSubmission tick %d: %v", i, err)
		}
		if sub.State.IsTerminal() {
			t.Fatalf("submission reached terminal state %s too early at tick %d, want it to stay non-terminal through tick %d",
				sub.State, i, missingWorkflowFailThreshold-1)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("threshold tick %d: %v", missingWorkflowFailThreshold, err)
	}
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED after %d consecutive missing-workflow ticks", sub.State, missingWorkflowFailThreshold)
	}
	if sub.Error == nil || sub.Error.Message == "" {
		t.Fatal("submission Error is nil/empty, want a persisted diagnostic")
	}
	if sub.Error.Code != "WORKFLOW_UNAVAILABLE" {
		t.Errorf("submission Error.Code = %q, want WORKFLOW_UNAVAILABLE", sub.Error.Code)
	}
	if !strings.Contains(sub.Error.Message, wfID) {
		t.Errorf("submission Error.Message = %q, want it to mention workflow id %q", sub.Error.Message, wfID)
	}

	// The terminal-submission guard (defect 1) must clean up the leftover
	// WAITING step instance on a subsequent tick.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("cleanup tick: %v", err)
	}
	si := getStepInstancesByStep(t, st, subID)["s1"]
	if si.State != model.StepStateSkipped {
		t.Errorf("s1 state = %s, want SKIPPED (terminalized by terminal-submission guard)", si.State)
	}
	if !strings.Contains(si.Error, "orphaned by terminal submission") {
		t.Errorf("s1 Error = %q, want it to mention orphaning", si.Error)
	}
}

// TestMissingWorkflow_CounterClearedAfterFail is a lightweight guard on the
// bookkeeping contract: missingWorkflowTicks must be cleared once a
// submission is failed, so the map doesn't grow unbounded across the
// scheduler's lifetime and a later reuse of the same submission ID (never
// happens in practice, but guards the invariant) doesn't start from a stale
// count.
func TestMissingWorkflow_CounterClearedAfterFail(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	wfID, subID := createPipeline(t, st, []model.Step{
		{ID: "s1", ToolInline: &model.Tool{ID: "t1", Class: "CommandLineTool", BaseCommand: []string{"echo"}}},
	}, map[string]any{}, 0)
	if err := st.DeleteWorkflow(ctx, wfID); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}

	for i := 0; i < missingWorkflowFailThreshold; i++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}
	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", sub.State)
	}
	if _, tracked := sched.missingWorkflowTicks[subID]; tracked {
		t.Errorf("missingWorkflowTicks still tracks %s after it was failed, want cleared", subID)
	}
}

// TestHealthyPipeline_UnaffectedByOrphanGuards is a regression check that the
// new per-si submission lookups added to advanceWaiting/dispatchReady/
// advanceSteps for the terminal-submission guard don't change behavior for a
// normal, healthy single-step pipeline: it must still complete in the usual
// handful of ticks.
func TestHealthyPipeline_UnaffectedByOrphanGuards(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 0

	_, subID := createPipeline(t, st, []model.Step{
		{ID: "step1", ToolInline: &model.Tool{
			ID:          "echo_tool",
			Class:       "CommandLineTool",
			BaseCommand: []string{"echo", "hello"},
		}},
	}, map[string]any{}, 0)

	sub := runToTerminal(t, sched, st, subID, 10)
	if sub.State != model.SubmissionStateCompleted {
		t.Fatalf("submission state = %s, want COMPLETED", sub.State)
	}

	si := getStepInstancesByStep(t, st, subID)["step1"]
	if si.State != model.StepStateCompleted {
		t.Fatalf("step1 state = %s, want COMPLETED", si.State)
	}
	if si.Error != "" {
		t.Errorf("step1 Error = %q, want empty on a healthy run", si.Error)
	}
}
