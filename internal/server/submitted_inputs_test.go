package server

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestGetSubmission_DetailReturnsSubmittedInputsUnchangedAfterRewrite is the
// handler-level guarantee for #239: the detail endpoint must keep returning
// the as-submitted inputs payload even after the scheduler's workspace
// pre-stage loop has rewritten the working `inputs` map (ws:// -> a
// transient file:// path) via store.UpdateSubmissionInputs.
func TestGetSubmission_DetailReturnsSubmittedInputsUnchangedAfterRewrite(t *testing.T) {
	srv, st := testServerWithStore()
	wfID := createTestWorkflow(t, srv)

	original := map[string]any{"reads_r1": "ws:///user@bvbrc/home/reads.fastq"}
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      original,
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != 201 {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	var created map[string]any
	json.Unmarshal(env.Data, &created)
	subID, _ := created["id"].(string)
	if subID == "" {
		t.Fatalf("created submission missing id, data=%v", created)
	}

	// Simulate the workspace pre-stage rewrite: Inputs is overwritten with a
	// transient staged path, exactly what internal/scheduler/workspace.go
	// does via store.UpdateSubmissionInputs.
	rewritten := map[string]any{"reads_r1": "file:///tmp/gowe-ws-stage/" + subID + "/reads.fastq"}
	if err := st.UpdateSubmissionInputs(context.Background(), subID, rewritten); err != nil {
		t.Fatalf("simulate prestage rewrite: %v", err)
	}

	env = doGet(t, srv, "/api/v1/submissions/"+subID)
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal detail response: %v", err)
	}

	gotSubmitted, ok := data["submitted_inputs"].(map[string]any)
	if !ok {
		t.Fatalf("submitted_inputs missing or wrong type in detail response: %v", data["submitted_inputs"])
	}
	if !reflect.DeepEqual(gotSubmitted, original) {
		t.Errorf("submitted_inputs = %v, want original %v (must survive prestage rewrite)", gotSubmitted, original)
	}

	gotInputs, ok := data["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing or wrong type in detail response: %v", data["inputs"])
	}
	if !reflect.DeepEqual(gotInputs, rewritten) {
		t.Errorf("inputs = %v, want rewritten %v", gotInputs, rewritten)
	}
}

// TestListSubmissions_PayloadOmitsSubmittedInputs verifies that the list
// endpoint response does not carry submitted_inputs, keeping list payloads
// slim as designed (#239).
func TestListSubmissions_PayloadOmitsSubmittedInputs(t *testing.T) {
	srv := testServer()
	createTestSubmission(t, srv)

	env := doGet(t, srv, "/api/v1/submissions/")
	var items []any
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("no submissions found in list response")
	}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if _, has := m["submitted_inputs"]; has {
			t.Errorf("list item unexpectedly contains submitted_inputs: %v", m)
		}
	}
}
