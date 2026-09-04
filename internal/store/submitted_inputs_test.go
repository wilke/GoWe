package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// TestCreateSubmission_SubmittedInputsDefaultsToInputs verifies that
// CreateSubmission captures a verbatim snapshot of Inputs into
// SubmittedInputs when the caller does not set SubmittedInputs explicitly.
func TestCreateSubmission_SubmittedInputsDefaultsToInputs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := sampleSubmission(wf.ID)
	sub.Inputs = map[string]any{"reads_r1": "ws:///user@bvbrc/home/reads.fastq"}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if !reflect.DeepEqual(got.SubmittedInputs, sub.Inputs) {
		t.Errorf("submitted_inputs = %v, want %v", got.SubmittedInputs, sub.Inputs)
	}
	if !reflect.DeepEqual(got.Inputs, sub.Inputs) {
		t.Errorf("inputs = %v, want %v", got.Inputs, sub.Inputs)
	}
}

// TestCreateSubmission_ExplicitSubmittedInputs verifies that when a caller
// sets SubmittedInputs explicitly (distinct from Inputs), CreateSubmission
// persists that value rather than defaulting to a copy of Inputs.
func TestCreateSubmission_ExplicitSubmittedInputs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := sampleSubmission(wf.ID)
	sub.Inputs = map[string]any{"reads_r1": "file:///tmp/gowe-ws-stage/sub_test-1/reads.fastq"}
	sub.SubmittedInputs = map[string]any{"reads_r1": "ws:///user@bvbrc/home/reads.fastq"}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if !reflect.DeepEqual(got.SubmittedInputs, sub.SubmittedInputs) {
		t.Errorf("submitted_inputs = %v, want %v", got.SubmittedInputs, sub.SubmittedInputs)
	}
	if !reflect.DeepEqual(got.Inputs, sub.Inputs) {
		t.Errorf("inputs = %v, want %v", got.Inputs, sub.Inputs)
	}
}

// TestUpdateSubmissionInputs_PreservesSubmittedInputs is the semantics
// guarantee test for #239: the scheduler's workspace pre-stage loop rewrites
// Inputs (ws:// -> transient file://) via UpdateSubmissionInputs, but
// SubmittedInputs must remain the original, untouched, verbatim payload.
func TestUpdateSubmissionInputs_PreservesSubmittedInputs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	original := map[string]any{"reads_r1": "ws:///user@bvbrc/home/reads.fastq"}
	sub := sampleSubmission(wf.ID)
	sub.Inputs = original
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	rewritten := map[string]any{"reads_r1": "file:///tmp/gowe-ws-stage/sub_test-1/reads.fastq"}
	if err := st.UpdateSubmissionInputs(ctx, sub.ID, rewritten); err != nil {
		t.Fatalf("update inputs: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if !reflect.DeepEqual(got.Inputs, rewritten) {
		t.Errorf("inputs = %v, want rewritten %v", got.Inputs, rewritten)
	}
	if !reflect.DeepEqual(got.SubmittedInputs, original) {
		t.Errorf("submitted_inputs = %v, want original %v (must survive UpdateSubmissionInputs untouched)", got.SubmittedInputs, original)
	}
}

// TestGetSubmission_SubmittedInputsNullRow verifies that a pre-#239 row with
// a NULL submitted_inputs column (simulated by inserting directly via SQL,
// bypassing insertSubmission) loads with a nil SubmittedInputs and no error.
func TestGetSubmission_SubmittedInputsNullRow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	_, err := st.db.ExecContext(ctx,
		`INSERT INTO submissions (id, workflow_id, workflow_name, state, inputs, outputs, labels, submitted_by, created_at, completed_at, user_token, token_expiry, auth_provider, parent_task_id, output_destination, output_state, submitted_inputs)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		"sub_pre239", wf.ID, wf.Name, string(model.SubmissionStatePending),
		`{"reads_r1":"file.fastq"}`, `{}`, `{}`, "user@test", "2026-01-01T00:00:00Z", nil,
		"", 0, "", "", "", "",
	)
	if err != nil {
		t.Fatalf("insert pre-239 row: %v", err)
	}

	got, err := st.GetSubmission(ctx, "sub_pre239")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got == nil {
		t.Fatal("got nil submission")
	}
	if got.SubmittedInputs != nil {
		t.Errorf("submitted_inputs = %v, want nil for pre-#239 row", got.SubmittedInputs)
	}
	if got.Inputs["reads_r1"] != "file.fastq" {
		t.Errorf("inputs not loaded correctly: %v", got.Inputs)
	}
}

// TestListSubmissions_SubmittedInputsNotInListPayload verifies that
// ListSubmissions does not select the submitted_inputs column, keeping list
// payloads slim (the field's json omitempty then also keeps it out of the
// serialized list response).
func TestListSubmissions_SubmittedInputsNotInListPayload(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := sampleSubmission(wf.ID)
	sub.Inputs = map[string]any{"reads_r1": "ws:///user@bvbrc/home/reads.fastq"}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	subs, _, err := st.ListSubmissions(ctx, model.DefaultListOptions())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("len = %d, want 1", len(subs))
	}
	if subs[0].SubmittedInputs != nil {
		t.Errorf("list submission SubmittedInputs = %v, want nil (list query must not select the column)", subs[0].SubmittedInputs)
	}
}
