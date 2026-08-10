package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// createFinalizeFixture creates a workflow, a PENDING submission, and a single
// StepInstance in the given state. Unlike createPipeline it lets the caller
// choose the step state so finalizeSubmissions can be exercised directly.
// It returns the submission ID.
func createFinalizeFixture(t *testing.T, st store.Store, stepState model.StepInstanceState) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_" + uuid.New().String()
	subID := "sub_" + uuid.New().String()

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "finalize-test-workflow",
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
		t.Fatalf("CreateWorkflow: %v", err)
	}

	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}

	si := &model.StepInstance{
		ID:           "si_" + uuid.New().String(),
		SubmissionID: subID,
		StepID:       "s1",
		State:        stepState,
		Outputs:      map[string]any{},
		CreatedAt:    now,
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("CreateStepInstance: %v", err)
	}

	return subID
}

// TestFinalize_CancelledMidTick_NotResurrected is the scheduler-level
// regression test for the defect-3b clobber class: finalizeSubmissions holds a
// snapshot of the submission taken earlier in the tick; if a cancel lands
// between that read and the terminal write, the blind UpdateSubmission used to
// resurrect the CANCELLED submission to COMPLETED (or RUNNING via activation).
// The CAS variants must lose that race and leave CANCELLED in place.
func TestFinalize_CancelledMidTick_NotResurrected(t *testing.T) {
	tests := []struct {
		name string
		// stepState drives which finalize branch fires: a terminal step makes
		// finalizeSubmissions attempt the terminal CAS write; an active step
		// makes it attempt the PENDING→RUNNING activation.
		stepState model.StepInstanceState
	}{
		{"terminal finalize loses to cancel", model.StepStateCompleted},
		{"pending activation loses to cancel", model.StepStateRunning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, st := testSetup(t)
			ctx := context.Background()

			subID := createFinalizeFixture(t, st, tt.stepState)

			// Materialize the mid-tick race: prime the tick cache with the
			// still-PENDING submission (the snapshot phase 6 would hold), ...
			sched.cache = newTickCache()
			cached, err := sched.cache.getSubmission(ctx, st, subID)
			if err != nil {
				t.Fatalf("prime cache: %v", err)
			}
			if cached.State != model.SubmissionStatePending {
				t.Fatalf("cached state = %q, want PENDING", cached.State)
			}

			// ... then cancel the submission behind the scheduler's back.
			cancelled, err := st.GetSubmission(ctx, subID)
			if err != nil {
				t.Fatalf("GetSubmission: %v", err)
			}
			cancelled.State = model.SubmissionStateCancelled
			if err := st.UpdateSubmission(ctx, cancelled); err != nil {
				t.Fatalf("UpdateSubmission(cancel): %v", err)
			}

			// Run phase 6 against the stale snapshot.
			if err := sched.finalizeSubmissions(ctx, map[string]bool{subID: true}); err != nil {
				t.Fatalf("finalizeSubmissions: %v", err)
			}

			got, err := st.GetSubmission(ctx, subID)
			if err != nil {
				t.Fatalf("GetSubmission: %v", err)
			}
			if got.State != model.SubmissionStateCancelled {
				t.Fatalf("after finalizeSubmissions: state = %q, want CANCELLED (resurrected)", got.State)
			}

			// A subsequent full tick must not touch the cancelled submission either.
			if err := sched.Tick(ctx); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			got, err = st.GetSubmission(ctx, subID)
			if err != nil {
				t.Fatalf("GetSubmission: %v", err)
			}
			if got.State != model.SubmissionStateCancelled {
				t.Errorf("after Tick: state = %q, want CANCELLED", got.State)
			}
		})
	}
}

// TestFinalize_PaginationBeyond100 verifies that finalizeSubmissions visits
// every candidate submission, not just the first Limit-100 page: 120 PENDING
// submissions with terminal steps must all finalize in a single tick.
func TestFinalize_PaginationBeyond100(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	const n = 120 // > model.MaxListLimit, forcing a second page
	subIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		subIDs = append(subIDs, createFinalizeFixture(t, st, model.StepStateCompleted))
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	var notFinalized int
	for _, subID := range subIDs {
		sub, err := st.GetSubmission(ctx, subID)
		if err != nil {
			t.Fatalf("GetSubmission(%s): %v", subID, err)
		}
		if sub.State != model.SubmissionStateCompleted {
			notFinalized++
		}
	}
	if notFinalized > 0 {
		t.Errorf("%d of %d submissions not finalized (Limit-100 page starvation)", notFinalized, n)
	}
}
