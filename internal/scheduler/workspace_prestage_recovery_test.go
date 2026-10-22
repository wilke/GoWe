package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// seedStuckRunningSubmission seeds a submission directly in the exact stuck
// shape #269 left rows in after the pre-fix deadlock: RUNNING, pre-staging
// started but never completed, a ws:// input, and a READY step instance with
// no tasks. It bypasses the scheduler entirely (no ticks) so the recovery
// tests below start from the stuck shape itself, not from re-deriving it via
// a lucky interleaving.
func seedStuckRunningSubmission(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	started := now.Add(-time.Minute)

	wfID := "wf_stuck_running"
	subID := "sub_stuck_running"

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "stuck-running-test",
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
		ID:                subID,
		WorkflowID:        wfID,
		WorkflowName:      wf.Name,
		State:             model.SubmissionStateRunning,
		UserToken:         "fake-token",
		PrestageStartedAt: &started,
		Inputs: map[string]any{
			"reads": map[string]any{
				"class":    "File",
				"location": "ws:///user@bvbrc/home/reads.fastq",
			},
		},
		Outputs:   map[string]any{},
		Labels:    map[string]string{},
		CreatedAt: started,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	si := &model.StepInstance{
		ID:           "si_stuck_running",
		SubmissionID: subID,
		StepID:       "s1",
		State:        model.StepStateReady,
		Outputs:      map[string]any{},
		CreatedAt:    started,
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}

	return subID
}

// flakyThenSucceedsStager builds a real *staging.WorkspaceStager (required by
// wsStagerInterface.WithToken's concrete return type) backed by an httptest
// server that fails the first failCount attempts to resolve the ws:// input's
// metadata (a non-retryable JSON-RPC error, so neither the stager's own
// retry loop nor the bvbrc client's adds delay) and succeeds from then on,
// serving a plain-file StageIn round trip identical in shape to
// workspace_attribution_test.go's cancelingStager.
func flakyThenSucceedsStager(t *testing.T, failCount int) *staging.WorkspaceStager {
	t.Helper()

	var calls int32
	var tsURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "Workspace.get":
			n := atomic.AddInt32(&calls, 1)
			if int(n) <= failCount {
				// Non-retryable JSON-RPC error (method-not-found is outside
				// the -32000..-32099 "may be retryable" range and is not
				// ErrCodeInternalError — see pkg/bvbrc.IsRetryable), so this
				// fails StageIn's single attempt immediately without the
				// client itself retrying with backoff.
				fmt.Fprint(w, `{"id":"1","version":"1.1","error":{"code":-32601,"name":"MethodNotFound","message":"transient test failure"}}`)
				return
			}
			// Metadata-only get: a plain file object (not a folder).
			fmt.Fprint(w, `{"id":"1","version":"1.1","result":[[[["reads.fastq","reads","/user@bvbrc/home/","2026-08-20T12:00:00Z","uuid1","user@bvbrc",0,{},{},"o","n"],""]]]}`)
		default:
			// Workspace.get_download_url JSON-RPC response: [[url]].
			fmt.Fprintf(w, `{"id":"1","version":"1.1","result":[["%s/download"]]}`, tsURL)
		}
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("staged-file-content"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tsURL = ts.URL

	return staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: ts.URL,
		Timeout:      5 * time.Second,
		MaxRetries:   1, // Single attempt per StageIn call, like unreachableStager.
	}, slog.Default())
}

// TestPrestageRecovery_RunningIncompleteReStagesAndDispatches is the
// recovery half of #269: a submission already stuck in the exact post-#268
// shape (RUNNING, prestage_started_at set, prestage_completed_at nil, a
// READY step, 0 tasks) must be picked back up by prestageWorkspaceInputs on
// the very next tick — not ignored forever because it is no longer PENDING —
// and once pre-staging succeeds, its already-READY step must dispatch in the
// same run.
func TestPrestageRecovery_RunningIncompleteReStagesAndDispatches(t *testing.T) {
	l, st := testSetup(t)
	l.config.MaxRetries = 0
	l.SetWorkspaceStager(flakyThenSucceedsStager(t, 1)) // Fails once, then succeeds.
	ctx := context.Background()

	subID := seedStuckRunningSubmission(t, st)

	// Tick 1: still RUNNING+incomplete going in. The stuck row must be picked
	// up (not skipped as "not PENDING") and this attempt fails (flaky stager's
	// first call), but the failure must be recorded rather than silently
	// dropped, and the row must still be RUNNING+incomplete afterward, not
	// abandoned.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	mid, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission after tick 1: %v", err)
	}
	if mid.State != model.SubmissionStateRunning {
		t.Fatalf("state after tick 1 = %q, want RUNNING (still incomplete, must not be abandoned)", mid.State)
	}
	if mid.PrestageCompletedAt != nil {
		t.Fatalf("prestage_completed_at after tick 1 = %v, want nil (first attempt fails)", mid.PrestageCompletedAt)
	}
	reads, _ := mid.Inputs["reads"].(map[string]any)
	if loc, _ := reads["location"].(string); loc != "ws:///user@bvbrc/home/reads.fastq" {
		t.Fatalf("inputs[reads].location after tick 1 = %q, want unchanged ws:// location", loc)
	}

	// Tick 2: the stuck row is picked up again (the whole point of #269's
	// fix) and this attempt succeeds: inputs rewrite to file://,
	// prestage_completed_at is stamped, and the already-READY step dispatches
	// (and, via the synchronous local executor, completes) in the same tick.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	final, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission after tick 2: %v", err)
	}
	if final.PrestageCompletedAt == nil {
		t.Fatal("expected prestage_completed_at to be stamped after the successful re-stage")
	}
	reads, ok := final.Inputs["reads"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing reads entry: %#v", final.Inputs)
	}
	if loc, _ := reads["location"].(string); loc == "" || loc[:7] != "file://" {
		t.Fatalf("inputs[reads].location = %q, want a file:// location after re-stage", loc)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1 (the READY step must dispatch once pre-staging completes)", len(tasks))
	}
	if tasks[0].State != model.TaskStateSuccess {
		t.Errorf("task.State = %q, want SUCCESS", tasks[0].State)
	}
	if final.State != model.SubmissionStateCompleted {
		t.Errorf("state = %q, want COMPLETED", final.State)
	}
}

// TestPrestageRecovery_RunningIncompleteFailsAfterThreshold is the
// give-up-from-RUNNING sibling of TestPrestageWorkspaceInputs_FailsAfterThreshold:
// a submission stuck RUNNING with incomplete pre-staging must still reach
// PRESTAGE_FAILED after prestageFailThreshold consecutive failed ticks — the
// bounded-retry give-up must work starting from RUNNING, not just PENDING.
func TestPrestageRecovery_RunningIncompleteFailsAfterThreshold(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := seedStuckRunningSubmission(t, st)

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
	if final.Error == nil || final.Error.Code != PrestageFailedCode {
		t.Fatalf("Error = %+v, want code %q", final.Error, PrestageFailedCode)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("tasks = %d, want 0", len(tasks))
	}
}

// TestPrestageRecovery_RunningIncompleteCancelNotClobbered is the
// RUNNING-incomplete sibling of
// TestPrestageWorkspaceInputs_FailureCancelNotClobbered: a submission
// cancelled while stuck RUNNING with incomplete pre-staging, one tick before
// the failure counter would otherwise reach prestageFailThreshold, must stay
// CANCELLED — the concurrent cancel wins over both the CAS-guarded stamp
// writes in prestageWorkspaceInputs and failPrestage's own
// finalizeSubmissionCAS.
func TestPrestageRecovery_RunningIncompleteCancelNotClobbered(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := seedStuckRunningSubmission(t, st)

	for i := 0; i < prestageFailThreshold-1; i++ {
		if err := l.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i+1, err)
		}
	}

	cancelled, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if cancelled.State != model.SubmissionStateRunning {
		t.Fatalf("state before cancel = %q, want RUNNING (still stuck, not yet failed)", cancelled.State)
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
		t.Fatalf("state = %q, want CANCELLED (must not be resurrected/overwritten by the recovery-path writes)", final.State)
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
