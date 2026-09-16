package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestPrestageWorkspaceInputs_FailsAfterThreshold is the #267 fail-fast
// regression: once server-side pre-staging has failed prestageFailThreshold
// consecutive ticks in a row, the submission FAILs with PRESTAGE_FAILED
// instead of retrying forever (or — pre-#267 — dispatching anyway with
// unstaged ws:// inputs). No tasks are ever created for it: dispatchReady
// defers this submission's READY step instances the whole time
// (PrestageStartedAt set, PrestageCompletedAt nil), and the terminal check
// takes over once it fails.
func TestPrestageWorkspaceInputs_FailsAfterThreshold(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmission(t, st)

	var final *model.Submission
	for i := 0; i < prestageFailThreshold; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
		var err error
		final, err = st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("get submission: %v", err)
		}
	}

	if final.State != model.SubmissionStateFailed {
		t.Fatalf("state = %q, want FAILED after %d failed attempts", final.State, prestageFailThreshold)
	}
	if final.Error == nil {
		t.Fatal("expected Error to be set")
	}
	if final.Error.Code != PrestageFailedCode {
		t.Errorf("Error.Code = %q, want %q", final.Error.Code, PrestageFailedCode)
	}
	const wantPath = "/user@bvbrc/home/reads.fastq"
	if !strings.Contains(final.Error.Message, wantPath) {
		t.Errorf("Error.Message = %q, want it to name the ws path %q", final.Error.Message, wantPath)
	}
	if final.Error.Context == nil {
		t.Fatal("expected Error.Context to be set")
	}
	if final.Error.Context.Location != "ws://"+wantPath {
		t.Errorf("Error.Context.Location = %q, want %q", final.Error.Context.Location, "ws://"+wantPath)
	}
	if final.Error.Context.Error == "" {
		t.Error("expected Error.Context.Error (the underlying stager error) to be non-empty")
	}
	if final.Error.Context.Attempts != prestageFailThreshold {
		t.Errorf("Error.Context.Attempts = %d, want %d", final.Error.Context.Attempts, prestageFailThreshold)
	}
	if final.CompletedAt == nil {
		t.Error("expected CompletedAt to be stamped")
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want 0 (no task may ever be created for a submission whose pre-stage fails)", len(tasks))
	}
}

// TestPrestageWorkspaceInputs_BelowThresholdStaysPending confirms the
// bounded-retry counter does not trip early: with fewer than
// prestageFailThreshold failed ticks, the submission is still PENDING and
// unFAILed (this is also exercised, for exactly 2 ticks, by
// TestPrestageWorkspaceInputs_StartedStampedOnceAcrossRetries — this test
// pins the boundary at threshold-1).
func TestPrestageWorkspaceInputs_BelowThresholdStaysPending(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmission(t, st)

	for i := 0; i < prestageFailThreshold-1; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub.State != model.SubmissionStatePending {
		t.Fatalf("state = %q, want PENDING after %d failed attempts (threshold is %d)",
			sub.State, prestageFailThreshold-1, prestageFailThreshold)
	}
	if sub.Error != nil {
		t.Errorf("Error = %+v, want nil before the threshold is reached", sub.Error)
	}
}

// TestPrestageWorkspaceInputs_FailureCancelNotClobbered is the failure-path
// sibling of TestPrestageWorkspaceInputs_CancelNotClobbered: a submission
// cancelled after prestageFailThreshold-1 failed ticks (i.e. the very next
// tick would otherwise reach the give-up threshold and FAIL it) must stay
// CANCELLED — the concurrent cancel wins, via failPrestage's
// finalizeSubmissionCAS guard (store.FinalizeSubmission: "state NOT IN
// terminal").
func TestPrestageWorkspaceInputs_FailureCancelNotClobbered(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmission(t, st)

	for i := 0; i < prestageFailThreshold-1; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	cancelled, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	cancelled.State = model.SubmissionStateCancelled
	now := time.Now().UTC()
	cancelled.CompletedAt = &now
	if _, err := st.FinalizeSubmission(ctx, cancelled); err != nil {
		t.Fatalf("finalize (cancel) submission: %v", err)
	}

	// This tick's failed attempt would push the counter to prestageFailThreshold.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick %d: %v", prestageFailThreshold, err)
	}

	final, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if final.State != model.SubmissionStateCancelled {
		t.Fatalf("state = %q, want CANCELLED (must not be resurrected/overwritten by the fail-fast write)", final.State)
	}
	if final.Error != nil && final.Error.Code == PrestageFailedCode {
		t.Errorf("Error = %+v, want no PRESTAGE_FAILED error to have landed over the cancel", final.Error)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want 0", len(tasks))
	}
}

// TestDispatchReady_PassthroughModeDispatchesWithWSInputs is the passthrough
// (wsStager == nil) regression: #267's dispatch gate must never engage when
// server-side staging isn't configured at all — dispatchReady keeps
// dispatching READY steps immediately even though their inputs still carry
// ws:// locations, exactly as before this change (the worker is expected to
// resolve ws:// itself in this deployment mode).
func TestDispatchReady_PassthroughModeDispatchesWithWSInputs(t *testing.T) {
	l, st := testSetup(t)
	l.config.MaxRetries = 0
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "echo_step",
			ToolInline: &model.Tool{
				ID:          "echo_tool",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hello"},
			},
		},
	}
	inputs := map[string]any{
		"reads": map[string]any{
			"class":    "File",
			"location": "ws:///user@bvbrc/home/reads.fastq",
		},
	}
	_, subID := createPipeline(t, st, steps, inputs, 0)

	// l.wsStager is never set: passthrough mode.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1 (passthrough mode must dispatch immediately, unchanged by #267)", len(tasks))
	}
}
