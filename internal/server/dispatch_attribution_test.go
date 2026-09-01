package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestWorkerTaskComplete_StageMsRoundTrip is the #184 PR2 round-trip test:
// a worker's terminal report carrying stage_in_ms/stage_out_ms persists into
// the tasks table and surfaces through GET .../timing as stage_in_s/
// stage_out_s, with compute_s = run_s - stage_in_s - stage_out_s.
func TestWorkerTaskComplete_StageMsRoundTrip(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)

	_, subID := createTestSubmission(t, srv)
	taskID := "task_stage_rt"

	// Seed a QUEUED worker task with DispatchedAt already stamped (as
	// submitAndUpdateTask would have done at dispatch time).
	dispatchedAt := time.Now().UTC().Add(-time.Second)
	task := &model.Task{
		ID:           taskID,
		SubmissionID: subID,
		StepID:       "step1",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		ExternalID:   taskID,
		DispatchedAt: &dispatchedAt,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
	}
	if err := srv.store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed queued worker task: %v", err)
	}

	checked := checkoutTask(t, srv, workerID)
	if checked.ID != taskID {
		t.Fatalf("checked out %s, want %s", checked.ID, taskID)
	}
	if checked.DispatchedAt == nil {
		t.Fatal("expected checkout response to carry DispatchedAt")
	}

	// Report completion with stage_in_ms/stage_out_ms (the new fields).
	body := `{"state":"SUCCESS","exit_code":0,"stdout":"","stderr":"","outputs":{},"stage_in_ms":1500,"stage_out_ms":250}`
	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", body)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	// Verify the columns persisted directly.
	persisted, err := srv.store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if persisted.StageInMs == nil || *persisted.StageInMs != 1500 {
		t.Errorf("stage_in_ms = %v, want 1500", persisted.StageInMs)
	}
	if persisted.StageOutMs == nil || *persisted.StageOutMs != 250 {
		t.Errorf("stage_out_ms = %v, want 250", persisted.StageOutMs)
	}

	// Verify they surface through the timing view.
	env := doGet(t, srv, "/api/v1/submissions/"+subID+"/timing")
	var rep timingReport
	if err := json.Unmarshal(env.Data, &rep); err != nil {
		t.Fatalf("unmarshal timing report: %v", err)
	}

	var row *taskTiming
	for i := range rep.Tasks {
		if rep.Tasks[i].TaskID == taskID {
			row = &rep.Tasks[i]
		}
	}
	if row == nil {
		t.Fatalf("task %s not found in timing report", taskID)
	}
	if row.StageInS == nil || *row.StageInS != 1.5 {
		t.Errorf("stage_in_s = %v, want 1.5", row.StageInS)
	}
	if row.StageOutS == nil || *row.StageOutS != 0.25 {
		t.Errorf("stage_out_s = %v, want 0.25", row.StageOutS)
	}
	if row.DispatchS == nil {
		t.Fatal("expected dispatch_s to be present")
	}
	if row.RunS == nil {
		t.Fatal("expected run_s to be present for a SUCCESS task")
	}
	if row.ComputeS == nil {
		t.Fatal("expected compute_s to be present")
	}
	wantCompute := *row.RunS - *row.StageInS - *row.StageOutS
	if wantCompute < 0 {
		wantCompute = 0
	}
	if diff := *row.ComputeS - wantCompute; diff > 0.002 || diff < -0.002 {
		t.Errorf("compute_s = %v, want ~%v (run=%v stage_in=%v stage_out=%v)",
			*row.ComputeS, wantCompute, *row.RunS, *row.StageInS, *row.StageOutS)
	}
}

// TestWorkerTaskComplete_StageMsAbsent_VersionSkew confirms an old worker's
// report (no stage_in_ms/stage_out_ms fields at all) leaves the columns NULL
// rather than erroring or zeroing them — the version-skew safety story.
func TestWorkerTaskComplete_StageMsAbsent_VersionSkew(t *testing.T) {
	srv := testServer()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	taskID := seedQueuedWorkerTask(t, srv, subID, "task_stage_skew")
	checkoutTask(t, srv, workerID)

	body := `{"state":"SUCCESS","exit_code":0,"stdout":"","stderr":"","outputs":{}}`
	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+taskID+"/complete", body)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	persisted, err := srv.store.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if persisted.StageInMs != nil {
		t.Errorf("stage_in_ms = %v, want nil (old worker never sent it)", persisted.StageInMs)
	}
	if persisted.StageOutMs != nil {
		t.Errorf("stage_out_ms = %v, want nil (old worker never sent it)", persisted.StageOutMs)
	}
}
