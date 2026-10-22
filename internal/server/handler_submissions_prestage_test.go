package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// failUntilSignaledStager builds a real *staging.WorkspaceStager pointed at
// an httptest server that answers every Workspace RPC call with HTTP 500
// while its returned "failing" flag is true, and successfully serves a
// single plain file download once the caller flips it to false. Using an
// explicit flag (rather than counting calls) sidesteps the underlying
// bvbrc.Client's own internal transient-failure retries — the whole first
// scheduler tick fails deterministically, then the whole second tick
// succeeds deterministically, regardless of how many HTTP round-trips one
// StageIn call happens to make.
func failUntilSignaledStager(t *testing.T) (*staging.WorkspaceStager, *atomic.Bool) {
	t.Helper()

	var failing atomic.Bool
	failing.Store(true)
	var tsURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "Workspace.get":
			fmt.Fprint(w, `{"id":"1","version":"1.1","result":[[[["genome.fasta","reads","/user@bvbrc/home/","2026-08-20T12:00:00Z","uuid1","user@bvbrc",0,{},{},"o","n"],""]]]}`)
		default:
			fmt.Fprintf(w, `{"id":"1","version":"1.1","result":[["%s/download"]]}`, tsURL)
		}
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("staged-genome-content"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tsURL = ts.URL

	stager := staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: ts.URL,
		Timeout:      5 * time.Second,
		MaxRetries:   1,
	}, slog.Default())
	return stager, &failing
}

// seedPrestageFailedSubmission creates a workflow, a FAILED submission with
// a PRESTAGE_FAILED error and a ws:// input, and one StepInstance whose
// DependsOn names a step ID absent from the workflow — simulating what
// scheduler.failPrestage (#267) leaves behind after exhausting its retries,
// without having to actually drive prestageFailThreshold real ticks. The
// dangling dependency (same trick as scheduler's wsInputSubmission test
// fixture) keeps the step WAITING forever once retried, so a scheduler tick
// against this fixture never finalizes the submission via an unrelated
// all-steps-terminal path — the test can observe pre-stage state in
// isolation.
func seedPrestageFailedSubmission(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)

	wfID := "wf_prestage_retry"
	subID := "sub_prestage_failed"

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "prestage-retry-test",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{ID: "s1", ToolRef: "#t", DependsOn: []string{"blocker"}},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	sub := &model.Submission{
		ID:           subID,
		WorkflowID:   wfID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStateFailed,
		UserToken:    secretsTestToken,
		SubmittedBy:  "secrets-tester",
		Inputs: map[string]any{
			"reads_r1": map[string]any{
				"class":    "File",
				"location": "ws:///user@bvbrc/home/genome.fasta",
			},
		},
		Outputs:           map[string]any{},
		Labels:            map[string]string{},
		CreatedAt:         now,
		CompletedAt:       &now,
		PrestageStartedAt: &started,
		Error: &model.SubmissionError{
			Code:    scheduler.PrestageFailedCode,
			Message: "pre-staging workspace input /user@bvbrc/home/genome.fasta failed: workspace stager: determine object type: boom",
			Context: &model.SubmissionErrDetail{
				Location: "ws:///user@bvbrc/home/genome.fasta",
				Error:    "boom",
				Attempts: 10,
			},
		},
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("seed prestage-failed submission: %v", err)
	}

	blocker := &model.StepInstance{
		ID: "si_blocker_retry", SubmissionID: subID, StepID: "blocker",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := st.CreateStepInstance(ctx, blocker); err != nil {
		t.Fatalf("seed blocker step instance: %v", err)
	}
	si := &model.StepInstance{
		ID: "si_s1_retry", SubmissionID: subID, StepID: "s1",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("seed step instance: %v", err)
	}

	return subID
}

// TestHandleRetrySubmission_PrestageFailedResetsToPending is the #267 retry
// regression: retrying a PRESTAGE_FAILED submission must reset the pre-stage
// markers and Error and route it back through PENDING (not RUNNING), so
// prestageWorkspaceInputs picks it up fresh. A subsequent scheduler tick
// against a stager that fails once then succeeds proves pre-staging actually
// re-runs and the submission proceeds (PrestageCompletedAt stamped, the
// ws:// input rewritten to file://).
func TestHandleRetrySubmission_PrestageFailedResetsToPending(t *testing.T) {
	srv, st := testServerWithStore(WithServerSideStaging(true))
	subID := seedPrestageFailedSubmission(t, st)

	w, env := doPutAs(t, srv, "/api/v1/submissions/"+subID+"/retry", "", secretsTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("retry: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	if data["state"] != string(model.SubmissionStatePending) {
		t.Fatalf("response state = %v, want PENDING", data["state"])
	}

	sub, err := st.GetSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub.State != model.SubmissionStatePending {
		t.Fatalf("state = %q, want PENDING", sub.State)
	}
	if sub.Error != nil {
		t.Fatalf("Error = %+v, want nil (cleared by retry)", sub.Error)
	}
	if sub.PrestageStartedAt != nil {
		t.Errorf("PrestageStartedAt = %v, want nil (reset by retry)", sub.PrestageStartedAt)
	}
	if sub.PrestageCompletedAt != nil {
		t.Errorf("PrestageCompletedAt = %v, want nil (reset by retry)", sub.PrestageCompletedAt)
	}

	// Now prove the pre-stage loop actually runs again: tick a real
	// scheduler against the same store with a stager that fails (tick 1)
	// then, once signaled, succeeds (tick 2).
	logger := slog.Default()
	reg := executor.NewRegistry(logger)
	sched := scheduler.NewLoop(st, reg, scheduler.DefaultConfig(), logger)
	stager, failing := failUntilSignaledStager(t)
	sched.SetWorkspaceStager(stager)

	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	mid, err := st.GetSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("get submission after tick 1: %v", err)
	}
	if mid.State != model.SubmissionStatePending {
		t.Fatalf("state after tick 1 = %q, want still PENDING (first re-stage attempt fails)", mid.State)
	}
	if mid.PrestageCompletedAt != nil {
		t.Fatalf("PrestageCompletedAt after tick 1 = %v, want nil", mid.PrestageCompletedAt)
	}

	failing.Store(false)
	if err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	final, err := st.GetSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("get submission after tick 2: %v", err)
	}
	if final.PrestageCompletedAt == nil {
		t.Fatal("PrestageCompletedAt after tick 2 = nil, want stamped (pre-stage succeeded)")
	}
	// The dangling "blocker" dependency keeps step s1 WAITING forever, so the
	// submission itself never finalizes — it stays PENDING, distinct from
	// (and not clobbered back into) FAILED. That isolates the assertion to
	// "did pre-staging itself succeed" without a real dispatch/executor in
	// the loop.
	if final.State != model.SubmissionStatePending {
		t.Fatalf("state after tick 2 = %q, want PENDING (pre-stage succeeded; step s1 stays blocked)", final.State)
	}
	reads, ok := final.Inputs["reads_r1"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing reads_r1: %#v", final.Inputs)
	}
	if loc, _ := reads["location"].(string); !strings.HasPrefix(loc, "file://") {
		t.Fatalf("inputs[reads_r1].location = %q, want a file:// location after successful re-stage", loc)
	}
}

// TestHandleRetrySubmission_OrdinaryFailureUnchanged confirms retrying a
// submission that failed for an ordinary (non-prestage) reason, with inputs
// already fully staged to file://, is unaffected by #267: it still resets
// straight to RUNNING, exactly as before.
func TestHandleRetrySubmission_OrdinaryFailureUnchanged(t *testing.T) {
	srv, st := testServerWithStore(WithServerSideStaging(true))
	wfID := createTestWorkflow(t, srv)

	now := time.Now().UTC()
	sub := &model.Submission{
		ID:           "sub_ordinary_failed",
		WorkflowID:   wfID,
		WorkflowName: "test-workflow",
		State:        model.SubmissionStateFailed,
		UserToken:    secretsTestToken,
		SubmittedBy:  "secrets-tester",
		Inputs: map[string]any{
			"reads_r1": map[string]any{
				"class":    "File",
				"location": "file:///tmp/staged/genome.fasta",
			},
		},
		Outputs:     map[string]any{},
		Labels:      map[string]string{},
		CreatedAt:   now,
		CompletedAt: &now,
		Error: &model.SubmissionError{
			Code:    "STEP_FAILED",
			Message: "step echo_step failed",
		},
	}
	if err := st.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("seed ordinary-failed submission: %v", err)
	}

	w, env := doPutAs(t, srv, "/api/v1/submissions/"+sub.ID+"/retry", "", secretsTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("retry: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	if data["state"] != string(model.SubmissionStateRunning) {
		t.Fatalf("response state = %v, want RUNNING (unchanged behavior)", data["state"])
	}

	got, err := st.GetSubmission(context.Background(), sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.State != model.SubmissionStateRunning {
		t.Fatalf("state = %q, want RUNNING", got.State)
	}
}

// TestHandleCreateSubmission_RejectsNonASCIIWorkspacePath is the #267
// submit-time validation regression: a ws:// input path containing a
// non-ASCII character (U+2010, the exact production incident) must be
// rejected with 400 VALIDATION_ERROR naming the input and the code point,
// not accepted and left to fail confusingly deep inside pre-staging.
func TestHandleCreateSubmission_RejectsNonASCIIWorkspacePath(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	badPath := "ws:///user@bvbrc/home/genome‐draft.fasta" // U+2010 hyphen
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"reads_r1": map[string]any{
				"class":    "File",
				"location": badPath,
			},
		},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil {
		t.Fatal("expected an error envelope")
	}
	if env.Error.Code != model.ErrValidation {
		t.Errorf("error code = %q, want %q", env.Error.Code, model.ErrValidation)
	}
	if !strings.Contains(env.Error.Message, "U+2010") {
		t.Errorf("error message = %q, want it to name U+2010", env.Error.Message)
	}
	if !strings.Contains(env.Error.Message, "reads_r1") {
		t.Errorf("error message = %q, want it to name the input id reads_r1", env.Error.Message)
	}
}

// TestHandleCreateSubmission_RejectsNonASCIIOutputDestination mirrors the
// input-side rejection for output_destination.
func TestHandleCreateSubmission_RejectsNonASCIIOutputDestination(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id":        wfID,
		"inputs":             map[string]any{"reads_r1": "test.fastq"},
		"output_destination": "ws:///user@bvbrc/home/results‐draft/",
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(env.Error.Message, "U+2010") {
		t.Errorf("error message = %q, want it to name U+2010", env.Error.Message)
	}
	if !strings.Contains(env.Error.Message, "output_destination") {
		t.Errorf("error message = %q, want it to name output_destination", env.Error.Message)
	}
}

// TestHandleCreateSubmission_AcceptsASCIIWorkspacePath confirms plain ASCII
// ws:// paths — including the "#", "%", "?" characters that are valid
// workspace path syntax and must NOT be rejected — are accepted.
func TestHandleCreateSubmission_AcceptsASCIIWorkspacePath(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"reads_r1": map[string]any{
				"class":    "File",
				"location": "ws:///user@bvbrc/home/genome#draft%2 (final)?.fasta",
			},
		},
	})
	w, _ := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}
}
