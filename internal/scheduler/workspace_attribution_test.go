package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// wsInputSubmission creates a workflow + PENDING submission with a step
// ("s1") that depends on a "blocker" step ID that has NO step definition in
// the workflow. areStepDependenciesSatisfied looks up dependencies by
// StepInstance (not by workflow step definition), so a StepInstance row for
// "blocker" keeps "s1" waiting (dependency exists but is non-terminal) —
// while "blocker" itself is never advanced by advanceWaiting (its stepDef
// lookup misses, so the phase skips it) and stays WAITING forever. Both
// stepinstances therefore never reach a terminal state, so the submission is
// never finalized — letting a test observe the scheduler across multiple
// ticks while it remains PENDING. The submission carries one ws:// File
// input and a UserToken, so prestageWorkspaceInputs picks it up every tick.
func wsInputSubmission(t *testing.T, st interface {
	CreateWorkflow(ctx context.Context, wf *model.Workflow) error
	CreateSubmission(ctx context.Context, sub *model.Submission) error
	CreateStepInstance(ctx context.Context, si *model.StepInstance) error
}) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_ws_test"
	subID := "sub_ws_test"

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "ws-test",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{ID: "s1", ToolRef: "#t", DependsOn: []string{"blocker"}},
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

	blocker := &model.StepInstance{
		ID:           "si_blocker",
		SubmissionID: subID,
		StepID:       "blocker",
		State:        model.StepStateWaiting,
		Outputs:      map[string]any{},
		CreatedAt:    now,
	}
	if err := st.CreateStepInstance(ctx, blocker); err != nil {
		t.Fatalf("create blocker step instance: %v", err)
	}

	si := &model.StepInstance{
		ID:           "si_ws_test",
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

// unreachableStager builds a real *staging.WorkspaceStager pointed at a
// closed local port, so every StageIn call fails fast (connection refused)
// with a single attempt — deterministic, no network/server needed. It
// satisfies wsStagerInterface (WithToken must return the concrete
// *staging.WorkspaceStager type).
func unreachableStager() *staging.WorkspaceStager {
	return staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: "http://127.0.0.1:1", // Nothing listens on port 1.
		Timeout:      2 * time.Second,
		MaxRetries:   1, // No internal retry sleep — first attempt fails immediately.
	}, slog.Default())
}

// TestPrestageWorkspaceInputs_StartedStampedOnceAcrossRetries is the
// multi-tick-retry regression: prestage_started_at must be stamped on the
// FIRST tick that attempts staging and never re-stamped on subsequent ticks,
// even though StageIn keeps failing (the submission stays PENDING and keeps
// being picked up every tick).
func TestPrestageWorkspaceInputs_StartedStampedOnceAcrossRetries(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmission(t, st)

	// Tick 1: StageIn fails (unreachable), but prestage_started_at is
	// stamped before the attempt.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	sub1, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub1.PrestageStartedAt == nil {
		t.Fatal("expected prestage_started_at to be stamped after tick 1")
	}
	if sub1.PrestageCompletedAt != nil {
		t.Fatal("expected prestage_completed_at to stay nil (staging failed)")
	}
	firstStamp := *sub1.PrestageStartedAt

	// Tick 2: still PENDING (the step can never become READY), so the
	// submission is picked up again — the stamp must NOT move.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	sub2, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub2.State != model.SubmissionStatePending {
		t.Fatalf("submission state = %q, want PENDING (test setup should keep it open)", sub2.State)
	}
	if sub2.PrestageStartedAt == nil || !sub2.PrestageStartedAt.Equal(firstStamp) {
		t.Errorf("prestage_started_at = %v, want unchanged %v", sub2.PrestageStartedAt, firstStamp)
	}
}

// TestPrestageWorkspaceInputs_CancelNotClobbered is the submission-level F-J
// clobber regression the advisor flagged: a submission cancelled between the
// prestage_started_at stamp and the (still in-flight, possibly slow) staging
// call must not be resurrected to PENDING by a subsequent full-row write.
// UpdateSubmissionIfState's CAS guard means an already-CANCELLED submission
// simply isn't touched again.
func TestPrestageWorkspaceInputs_CancelNotClobbered(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()

	subID := wsInputSubmission(t, st)

	// Tick 1 stamps prestage_started_at (and fails to stage, per the
	// unreachable stager).
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 1: %v", err)
	}

	// Simulate a concurrent cancel landing between ticks.
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

	// Tick 2: the submission is no longer PENDING, so it should not even be
	// picked up by prestageWorkspaceInputs's PENDING scan — but if a stale
	// in-memory snapshot were ever re-used, the CAS guard is the backstop.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick 2: %v", err)
	}

	final, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if final.State != model.SubmissionStateCancelled {
		t.Fatalf("state = %q, want CANCELLED (must not be resurrected)", final.State)
	}
}

// cancelingStager builds a real *staging.WorkspaceStager pointed at an
// httptest server that answers the network calls StageIn makes for a plain
// File input — first a metadata-only Workspace.get (StageIn's folder-vs-file
// check), then the Workspace.get_download_url JSON-RPC call, then the plain
// HTTP GET of the returned download URL — with a SUCCESSFUL download, but
// the GET handler first cancels subID in the store, synchronously, before
// writing any response bytes. Because the Go HTTP client only unblocks once
// the handler has produced a response, this deterministically interleaves
// the store write between StageIn's success return and the caller's next CAS
// attempt: no sleeps, no goroutines racing the scheduler tick.
func cancelingStager(t *testing.T, st interface {
	GetSubmission(ctx context.Context, id string) (*model.Submission, error)
	FinalizeSubmission(ctx context.Context, sub *model.Submission) (bool, error)
}, subID string) *staging.WorkspaceStager {
	t.Helper()

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
			// Metadata-only get: a plain file object (not a folder), so
			// StageIn takes the single-file path this test exercises.
			fmt.Fprint(w, `{"id":"1","version":"1.1","result":[[[["reads.fastq","reads","/user@bvbrc/home/","2026-08-20T12:00:00Z","uuid1","user@bvbrc",0,{},{},"o","n"],""]]]}`)
		default:
			// Workspace.get_download_url JSON-RPC response: [[url]].
			fmt.Fprintf(w, `{"id":"1","version":"1.1","result":[["%s/download"]]}`, tsURL)
		}
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		sub, err := st.GetSubmission(ctx, subID)
		if err != nil {
			t.Errorf("cancelingStager: get submission: %v", err)
			http.Error(w, "get failed", http.StatusInternalServerError)
			return
		}
		sub.State = model.SubmissionStateCancelled
		now := time.Now().UTC()
		sub.CompletedAt = &now
		if _, err := st.FinalizeSubmission(ctx, sub); err != nil {
			t.Errorf("cancelingStager: finalize (cancel): %v", err)
			http.Error(w, "cancel failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("staged-file-content"))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tsURL = ts.URL

	return staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: ts.URL,
		Timeout:      5 * time.Second,
		MaxRetries:   1,
	}, slog.Default())
}

// TestPrestageWorkspaceInputs_CancelDuringStageInNotClobbered is the
// deterministic scheduler-level interleave regression for the prestage
// completed-stamp CAS: a submission cancelled WHILE StageIn is in flight
// (between the started-stamp write and the completed-stamp write, both
// gated on the same PENDING check) must not have its cancellation clobbered
// back to PENDING, and the completed stamp must not be recorded for a run
// that no longer owns the submission.
func TestPrestageWorkspaceInputs_CancelDuringStageInNotClobbered(t *testing.T) {
	l, st := testSetup(t)
	ctx := context.Background()

	subID := wsInputSubmission(t, st)
	l.SetWorkspaceStager(cancelingStager(t, st, subID))

	// One tick: prestage_started_at is stamped (still PENDING), StageIn
	// succeeds but its handler cancels the submission first, then the
	// completed-stamp CAS below must find the row no longer PENDING and
	// reject the write.
	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	final, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if final.State != model.SubmissionStateCancelled {
		t.Fatalf("state = %q, want CANCELLED (must not be resurrected)", final.State)
	}
	if final.PrestageCompletedAt != nil {
		t.Fatalf("prestage_completed_at = %v, want nil (rejected write must not land)", final.PrestageCompletedAt)
	}
	// The rewritten file:// location must not have been persisted either:
	// prestageWorkspaceInputs skips UpdateSubmissionInputs when the
	// completed-stamp CAS is rejected, so the input must still carry its
	// original ws:// location.
	reads, ok := final.Inputs["reads"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing reads entry: %#v", final.Inputs)
	}
	if loc, _ := reads["location"].(string); loc != "ws:///user@bvbrc/home/reads.fastq" {
		t.Fatalf("inputs[reads].location = %q, want unchanged ws:// location (rewrite must not persist)", loc)
	}
}

// TestPoststageWorkspaceOutputs_StartedAndCompletedStamped confirms both
// poststage timestamps are stamped across the "uploading" -> "delivered"
// transition. The submission has no File outputs, so the per-file staging
// loop never touches the network (stageFileInTree finds nothing to stage)
// and the run reaches "delivered" even with an unreachable stager — the
// best-effort output-manifest upload failing is logged but non-fatal.
func TestPoststageWorkspaceOutputs_StartedAndCompletedStamped(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()
	now := time.Now().UTC()

	wf := &model.Workflow{
		ID: "wf_poststage", Name: "poststage-test", CWLVersion: "v1.2",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := &model.Submission{
		ID: "sub_poststage", WorkflowID: wf.ID, WorkflowName: wf.Name,
		State:             model.SubmissionStateCompleted,
		UserToken:         "fake-token",
		OutputDestination: "ws:///user@bvbrc/home/results/",
		Inputs:            map[string]any{},
		Outputs:           map[string]any{},
		Labels:            map[string]string{},
		CreatedAt:         now,
		CompletedAt:       &now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.PoststageStartedAt == nil {
		t.Fatal("expected poststage_started_at to be stamped")
	}
	if got.OutputState != "delivered" {
		t.Fatalf("output_state = %q, want delivered", got.OutputState)
	}
	if got.PoststageCompletedAt == nil {
		t.Fatal("expected poststage_completed_at to be stamped")
	}
	if got.PoststageCompletedAt.Before(*got.PoststageStartedAt) {
		t.Errorf("poststage_completed_at %v before poststage_started_at %v", got.PoststageCompletedAt, got.PoststageStartedAt)
	}
}

// TestPoststageWorkspaceOutputs_FailureStampsCompletedAt is the fix-2
// regression: when the output upload itself fails (not the no-token/
// bad-scheme early-outs), failOutputStaging must still stamp
// PoststageCompletedAt, closing the poststage window on the failure path —
// otherwise a submission whose upload failed mid-flight would show an
// open-ended (never-closed) poststage_s window forever.
func TestPoststageWorkspaceOutputs_FailureStampsCompletedAt(t *testing.T) {
	l, st := testSetup(t)
	l.SetWorkspaceStager(unreachableStager())
	ctx := context.Background()
	now := time.Now().UTC()

	wf := &model.Workflow{
		ID: "wf_poststage_fail", Name: "poststage-fail-test", CWLVersion: "v1.2",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := &model.Submission{
		ID: "sub_poststage_fail", WorkflowID: wf.ID, WorkflowName: wf.Name,
		State:             model.SubmissionStateCompleted,
		UserToken:         "fake-token",
		OutputDestination: "ws:///user@bvbrc/home/results/",
		Inputs:            map[string]any{},
		Outputs: map[string]any{
			"result": map[string]any{
				"class":    "File",
				"location": "file:///tmp/does-not-need-to-exist.txt",
				"basename": "does-not-need-to-exist.txt",
			},
		},
		Labels:      map[string]string{},
		CreatedAt:   now,
		CompletedAt: &now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	if err := l.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.OutputState != "upload_failed" {
		t.Fatalf("output_state = %q, want upload_failed", got.OutputState)
	}
	if got.State != model.SubmissionStateFailed {
		t.Fatalf("state = %q, want FAILED", got.State)
	}
	if got.PoststageStartedAt == nil {
		t.Fatal("expected poststage_started_at to be stamped")
	}
	if got.PoststageCompletedAt == nil {
		t.Fatal("expected poststage_completed_at to be stamped even on failure (closes the poststage window)")
	}
	if got.PoststageCompletedAt.Before(*got.PoststageStartedAt) {
		t.Errorf("poststage_completed_at %v before poststage_started_at %v", got.PoststageCompletedAt, got.PoststageStartedAt)
	}
}

// wsDirectoryInputSubmission is wsInputSubmission's sibling for a ws://
// Directory input instead of a File input, so prestageWorkspaceInputs'
// Directory-awareness (recursive StageIn) can be exercised at the scheduler
// level, not just inside pkg/staging.
func wsDirectoryInputSubmission(t *testing.T, st interface {
	CreateWorkflow(ctx context.Context, wf *model.Workflow) error
	CreateSubmission(ctx context.Context, sub *model.Submission) error
	CreateStepInstance(ctx context.Context, si *model.StepInstance) error
}, wsDir string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wfID := "wf_ws_dir_test"
	subID := "sub_ws_dir_test"

	wf := &model.Workflow{
		ID:         wfID,
		Name:       "ws-dir-test",
		CWLVersion: "v1.2",
		Steps: []model.Step{
			{ID: "s1", ToolRef: "#t", DependsOn: []string{"blocker"}},
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
		UserToken:    "un=tester|sig=abc",
		Inputs: map[string]any{
			"reads_dir": map[string]any{
				"class":    "Directory",
				"location": "ws://" + wsDir,
			},
		},
		Outputs:   map[string]any{},
		Labels:    map[string]string{},
		CreatedAt: now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	blocker := &model.StepInstance{
		ID: "si_blocker", SubmissionID: subID, StepID: "blocker",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := st.CreateStepInstance(ctx, blocker); err != nil {
		t.Fatalf("create blocker step instance: %v", err)
	}

	si := &model.StepInstance{
		ID: "si_ws_dir_test", SubmissionID: subID, StepID: "s1",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := st.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}

	return subID
}

// TestPrestageWorkspaceInputs_DirectoryInputRecursivelyStaged is the
// scheduler-level counterpart of pkg/staging's recursive-StageIn unit tests:
// a submission with a ws:// Directory input, pre-staged against a real fake
// Workspace service (bvbrctest), ends up with the whole tree downloaded
// under the per-submission stage directory and the input rewritten to a
// file:// location pointing at it.
func TestPrestageWorkspaceInputs_DirectoryInputRecursivelyStaged(t *testing.T) {
	f := bvbrctest.New(t)
	seedClient := bvbrc.NewClient(bvbrc.Config{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	const wsDir = "/tester@bvbrc/home/reads"
	ctx := context.Background()
	if _, err := seedClient.WorkspaceCreateFolder(ctx, wsDir); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := seedClient.WorkspaceUploadFile(ctx, wsDir+"/r1.fastq", []byte("read one"), bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := seedClient.WorkspaceCreateFolder(ctx, wsDir+"/sub"); err != nil {
		t.Fatalf("seed subfolder: %v", err)
	}
	if _, err := seedClient.WorkspaceUploadFile(ctx, wsDir+"/sub/r2.fastq", []byte("read two"), bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("seed nested file: %v", err)
	}

	l, st := testSetup(t)
	l.SetWorkspaceStager(staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		MaxRetries:   1,
	}, slog.Default()))

	subID := wsDirectoryInputSubmission(t, st, wsDir)

	if err := l.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	got, err := st.GetSubmission(context.Background(), subID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}

	readsDir, ok := got.Inputs["reads_dir"].(map[string]any)
	if !ok {
		t.Fatalf("inputs missing reads_dir entry: %#v", got.Inputs)
	}
	loc, _ := readsDir["location"].(string)
	if loc == "" || loc[:7] != "file://" {
		t.Fatalf("reads_dir.location = %q, want a file:// location (rewritten after successful pre-stage)", loc)
	}

	stageDir := loc[len("file://"):]
	for rel, want := range map[string]string{
		"r1.fastq":     "read one",
		"sub/r2.fastq": "read two",
	} {
		gotBytes, err := os.ReadFile(filepath.Join(stageDir, rel))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		if string(gotBytes) != want {
			t.Errorf("%s content = %q, want %q", rel, gotBytes, want)
		}
	}

	if got.PrestageCompletedAt == nil {
		t.Error("expected prestage_completed_at to be stamped after a successful directory pre-stage")
	}
}
