package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// --- Compare-and-set write tests ---

func TestFinalizeSubmission(t *testing.T) {
	tests := []struct {
		name        string
		initial     model.SubmissionState
		wantApplied bool
		wantState   model.SubmissionState
	}{
		{"applies over RUNNING", model.SubmissionStateRunning, true, model.SubmissionStateCompleted},
		{"refuses to overwrite CANCELLED", model.SubmissionStateCancelled, false, model.SubmissionStateCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()

			wf := sampleWorkflow()
			if err := st.CreateWorkflow(ctx, wf); err != nil {
				t.Fatalf("create workflow: %v", err)
			}
			sub := sampleSubmission(wf.ID)
			sub.State = tt.initial
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatalf("create submission: %v", err)
			}

			now := time.Now().UTC().Truncate(time.Millisecond)
			sub.State = model.SubmissionStateCompleted
			sub.CompletedAt = &now
			sub.Outputs = map[string]any{"genome": "annotated.gb"}

			applied, err := st.FinalizeSubmission(ctx, sub)
			if err != nil {
				t.Fatalf("finalize: %v", err)
			}
			if applied != tt.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tt.wantApplied)
			}

			got, err := st.GetSubmission(ctx, sub.ID)
			if err != nil {
				t.Fatalf("get submission: %v", err)
			}
			if got.State != tt.wantState {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
			if tt.wantApplied {
				if got.CompletedAt == nil {
					t.Error("expected completed_at to be set")
				}
				if got.Outputs["genome"] != "annotated.gb" {
					t.Errorf("outputs = %v, want genome=annotated.gb", got.Outputs)
				}
			} else {
				if got.CompletedAt != nil {
					t.Error("expected completed_at to stay unset")
				}
				if len(got.Outputs) != 0 {
					t.Errorf("outputs = %v, want empty", got.Outputs)
				}
			}
		})
	}
}

func TestActivateSubmission(t *testing.T) {
	tests := []struct {
		name        string
		initial     model.SubmissionState
		wantApplied bool
		wantState   model.SubmissionState
	}{
		{"applies from PENDING", model.SubmissionStatePending, true, model.SubmissionStateRunning},
		{"refuses from CANCELLED", model.SubmissionStateCancelled, false, model.SubmissionStateCancelled},
		{"refuses from RUNNING", model.SubmissionStateRunning, false, model.SubmissionStateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()

			wf := sampleWorkflow()
			if err := st.CreateWorkflow(ctx, wf); err != nil {
				t.Fatalf("create workflow: %v", err)
			}
			sub := sampleSubmission(wf.ID)
			sub.State = tt.initial
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatalf("create submission: %v", err)
			}

			applied, err := st.ActivateSubmission(ctx, sub.ID)
			if err != nil {
				t.Fatalf("activate: %v", err)
			}
			if applied != tt.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tt.wantApplied)
			}

			got, err := st.GetSubmission(ctx, sub.ID)
			if err != nil {
				t.Fatalf("get submission: %v", err)
			}
			if got.State != tt.wantState {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
		})
	}
}

func TestTerminalizeTask(t *testing.T) {
	tests := []struct {
		name        string
		initial     model.TaskState
		wantApplied bool
		wantState   model.TaskState
	}{
		{"applies over RUNNING", model.TaskStateRunning, true, model.TaskStateSuccess},
		// SKIPPED simulates a concurrent cancel that already terminalized the task.
		{"refuses to overwrite SKIPPED", model.TaskStateSkipped, false, model.TaskStateSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()

			wf := sampleWorkflow()
			if err := st.CreateWorkflow(ctx, wf); err != nil {
				t.Fatalf("create workflow: %v", err)
			}
			sub := sampleSubmission(wf.ID)
			if err := st.CreateSubmission(ctx, sub); err != nil {
				t.Fatalf("create submission: %v", err)
			}
			task := sampleTask(sub.ID)
			task.State = tt.initial
			if err := st.CreateTask(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			now := time.Now().UTC().Truncate(time.Millisecond)
			task.State = model.TaskStateSuccess
			task.CompletedAt = &now
			task.Outputs = map[string]any{"contigs": "contigs.fasta"}

			applied, err := st.TerminalizeTask(ctx, task)
			if err != nil {
				t.Fatalf("terminalize: %v", err)
			}
			if applied != tt.wantApplied {
				t.Errorf("applied = %v, want %v", applied, tt.wantApplied)
			}

			got, err := st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatalf("get task: %v", err)
			}
			if got.State != tt.wantState {
				t.Errorf("state = %q, want %q", got.State, tt.wantState)
			}
			if tt.wantApplied {
				if got.CompletedAt == nil {
					t.Error("expected completed_at to be set")
				}
				if got.Outputs["contigs"] != "contigs.fasta" {
					t.Errorf("outputs = %v, want contigs=contigs.fasta", got.Outputs)
				}
			} else {
				if got.CompletedAt != nil {
					t.Error("expected completed_at to stay unset")
				}
				if len(got.Outputs) != 0 {
					t.Errorf("outputs = %v, want empty", got.Outputs)
				}
			}
		})
	}
}

func TestListSubmissionsAwaitingOutputStaging(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	seq := 0
	mk := func(state model.SubmissionState, dest, outputState string) *model.Submission {
		seq++
		sub := sampleSubmission(wf.ID)
		sub.ID = fmt.Sprintf("sub_staging-%d", seq)
		sub.State = state
		sub.OutputDestination = dest
		sub.OutputState = outputState
		if err := st.CreateSubmission(ctx, sub); err != nil {
			t.Fatalf("create submission: %v", err)
		}
		return sub
	}

	eligible := mk(model.SubmissionStateCompleted, "ws:///user@bvbrc/home/out/", "")
	mk(model.SubmissionStateCompleted, "", "")                                    // no destination
	mk(model.SubmissionStateCompleted, "ws:///user@bvbrc/home/out/", "delivered") // already delivered
	mk(model.SubmissionStateRunning, "ws:///user@bvbrc/home/out/", "")            // not completed
	mk(model.SubmissionStateFailed, "ws:///user@bvbrc/home/out/", "")             // terminal but not completed

	got, err := st.ListSubmissionsAwaitingOutputStaging(ctx)
	if err != nil {
		t.Fatalf("list awaiting output staging: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d submissions, want 1", len(got))
	}
	if got[0].ID != eligible.ID {
		t.Errorf("got %s, want %s", got[0].ID, eligible.ID)
	}
}
