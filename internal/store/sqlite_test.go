package store

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sampleWorkflow() *model.Workflow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &model.Workflow{
		ID:          "wf_test-1",
		Name:        "test-workflow",
		Description: "A test workflow",
		CWLVersion:  "v1.2",
		RawCWL:      "cwlVersion: v1.2\n",
		Inputs: []model.WorkflowInput{
			{ID: "reads_r1", Type: "File", Required: true},
		},
		Outputs: []model.WorkflowOutput{
			{ID: "genome", Type: "File", OutputSource: "annotate/annotated_genome"},
		},
		Steps: []model.Step{
			{
				ID:        "assemble",
				ToolRef:   "#bvbrc-assembly",
				DependsOn: []string{},
				In:        []model.StepInput{{ID: "read1", Source: "reads_r1"}},
				Out:       []string{"contigs"},
				Hints:     &model.StepHints{BVBRCAppID: "GenomeAssembly2", ExecutorType: "bvbrc"},
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func sampleSubmission(workflowID string) *model.Submission {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &model.Submission{
		ID:           "sub_test-1",
		WorkflowID:   workflowID,
		WorkflowName: "test-workflow",
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{"reads_r1": "file.fastq"},
		Outputs:      map[string]any{},
		Labels:       map[string]string{"project": "test"},
		SubmittedBy:  "user@test",
		CreatedAt:    now,
	}
}

func sampleTask(submissionID string) *model.Task {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &model.Task{
		ID:           "task_test-1",
		SubmissionID: submissionID,
		StepID:       "assemble",
		State:        model.TaskStatePending,
		ExecutorType: model.ExecutorTypeBVBRC,
		BVBRCAppID:   "GenomeAssembly2",
		Inputs:       map[string]any{"read1": "file.fastq"},
		Outputs:      map[string]any{},
		DependsOn:    []string{},
		RetryCount:   0,
		MaxRetries:   3,
		CreatedAt:    now,
	}
}

// --- Migration tests ---

func TestMigrate_Idempotent(t *testing.T) {
	st := testStore(t)
	// Migrate a second time — should not error.
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// --- Workflow CRUD tests ---

func TestCreateAndGetWorkflow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	wf := sampleWorkflow()

	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil workflow")
	}
	if got.ID != wf.ID {
		t.Errorf("id = %q, want %q", got.ID, wf.ID)
	}
	if got.Name != wf.Name {
		t.Errorf("name = %q, want %q", got.Name, wf.Name)
	}
	if got.CWLVersion != wf.CWLVersion {
		t.Errorf("cwl_version = %q, want %q", got.CWLVersion, wf.CWLVersion)
	}
	if len(got.Inputs) != 1 {
		t.Errorf("inputs count = %d, want 1", len(got.Inputs))
	}
	if len(got.Steps) != 1 {
		t.Errorf("steps count = %d, want 1", len(got.Steps))
	}
	if got.Steps[0].Hints == nil || got.Steps[0].Hints.BVBRCAppID != "GenomeAssembly2" {
		t.Errorf("step hints not preserved")
	}
}

func TestGetWorkflow_NotFound(t *testing.T) {
	st := testStore(t)
	got, err := st.GetWorkflow(context.Background(), "wf_nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestListWorkflows_Empty(t *testing.T) {
	st := testStore(t)
	workflows, total, err := st.ListWorkflows(context.Background(), model.DefaultListOptions())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0", total)
	}
	if len(workflows) != 0 {
		t.Errorf("len = %d, want 0", len(workflows))
	}
}

func TestListWorkflows_Pagination(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Create 3 workflows with staggered timestamps.
	for i := 0; i < 3; i++ {
		wf := sampleWorkflow()
		wf.ID = fmt.Sprintf("wf_test-%d", i)
		wf.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		wf.UpdatedAt = wf.CreatedAt
		if err := st.CreateWorkflow(ctx, wf); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Page 1: limit 2.
	workflows, total, err := st.ListWorkflows(ctx, model.ListOptions{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("list page 1: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(workflows) != 2 {
		t.Errorf("page 1 len = %d, want 2", len(workflows))
	}

	// Page 2: offset 2.
	workflows, _, err = st.ListWorkflows(ctx, model.ListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(workflows) != 1 {
		t.Errorf("page 2 len = %d, want 1", len(workflows))
	}

	// Newest first order: first returned should be wf_test-2.
	workflows, _, _ = st.ListWorkflows(ctx, model.ListOptions{Limit: 10, Offset: 0})
	if workflows[0].ID != "wf_test-2" {
		t.Errorf("first = %q, want wf_test-2 (newest first)", workflows[0].ID)
	}
}

func TestUpdateWorkflow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)

	wf.Name = "updated-name"
	wf.Description = "updated description"
	wf.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)

	if err := st.UpdateWorkflow(ctx, wf); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := st.GetWorkflow(ctx, wf.ID)
	if got.Name != "updated-name" {
		t.Errorf("name = %q, want updated-name", got.Name)
	}
	if got.Description != "updated description" {
		t.Errorf("description = %q, want updated description", got.Description)
	}
}

func TestUpdateWorkflow_NotFound(t *testing.T) {
	st := testStore(t)
	wf := sampleWorkflow()
	wf.ID = "wf_nonexistent"
	if err := st.UpdateWorkflow(context.Background(), wf); err == nil {
		t.Error("expected error for nonexistent workflow")
	}
}

func TestDeleteWorkflow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)

	if err := st.DeleteWorkflow(ctx, wf.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, _ := st.GetWorkflow(ctx, wf.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteWorkflow_NotFound(t *testing.T) {
	st := testStore(t)
	if err := st.DeleteWorkflow(context.Background(), "wf_nonexistent"); err == nil {
		t.Error("expected error for nonexistent workflow")
	}
}

// --- Submission CRUD tests ---

func TestCreateAndGetSubmission(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)

	sub := sampleSubmission(wf.ID)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got == nil {
		t.Fatal("got nil submission")
	}
	if got.ID != sub.ID {
		t.Errorf("id = %q, want %q", got.ID, sub.ID)
	}
	if got.State != model.SubmissionStatePending {
		t.Errorf("state = %q, want PENDING", got.State)
	}
	if got.WorkflowID != wf.ID {
		t.Errorf("workflow_id = %q, want %q", got.WorkflowID, wf.ID)
	}
	if got.Labels["project"] != "test" {
		t.Errorf("labels = %v, want project=test", got.Labels)
	}
}

func TestGetSubmission_NotFound(t *testing.T) {
	st := testStore(t)
	got, err := st.GetSubmission(context.Background(), "sub_nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestListSubmissions(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)

	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	subs, total, err := st.ListSubmissions(ctx, model.DefaultListOptions())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(subs) != 1 {
		t.Errorf("len = %d, want 1", len(subs))
	}
}

func TestListSubmissions_StateFilter(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)

	// Create PENDING submission.
	sub1 := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub1)

	// Create RUNNING submission.
	sub2 := sampleSubmission(wf.ID)
	sub2.ID = "sub_test-2"
	sub2.State = model.SubmissionStateRunning
	st.CreateSubmission(ctx, sub2)

	opts := model.DefaultListOptions()
	opts.State = "PENDING"
	subs, total, err := st.ListSubmissions(ctx, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (only PENDING)", total)
	}
	if len(subs) != 1 || subs[0].ID != sub1.ID {
		t.Errorf("expected only pending submission")
	}
}

func TestUpdateSubmission(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	now := time.Now().UTC().Truncate(time.Millisecond)
	sub.State = model.SubmissionStateCompleted
	sub.CompletedAt = &now

	if err := st.UpdateSubmission(ctx, sub); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := st.GetSubmission(ctx, sub.ID)
	if got.State != model.SubmissionStateCompleted {
		t.Errorf("state = %q, want COMPLETED", got.State)
	}
	if got.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// --- Task tests ---

func TestCreateAndGetTask(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)
	task := sampleTask(sub.ID)

	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got == nil {
		t.Fatal("got nil task")
	}
	if got.ID != task.ID {
		t.Errorf("id = %q, want %q", got.ID, task.ID)
	}
	if got.State != model.TaskStatePending {
		t.Errorf("state = %q, want PENDING", got.State)
	}
	if got.ExecutorType != model.ExecutorTypeBVBRC {
		t.Errorf("executor = %q, want bvbrc", got.ExecutorType)
	}
	if got.BVBRCAppID != "GenomeAssembly2" {
		t.Errorf("bvbrc_app_id = %q, want GenomeAssembly2", got.BVBRCAppID)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	st := testStore(t)
	got, err := st.GetTask(context.Background(), "task_nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestListTasksBySubmission(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create two tasks.
	task1 := sampleTask(sub.ID)
	st.CreateTask(ctx, task1)

	task2 := sampleTask(sub.ID)
	task2.ID = "task_test-2"
	task2.StepID = "annotate"
	task2.CreatedAt = task1.CreatedAt.Add(time.Second)
	st.CreateTask(ctx, task2)

	tasks, err := st.ListTasksBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("len = %d, want 2", len(tasks))
	}
}

func TestUpdateTask(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)
	task := sampleTask(sub.ID)
	st.CreateTask(ctx, task)

	now := time.Now().UTC().Truncate(time.Millisecond)
	task.State = model.TaskStateSuccess
	task.StartedAt = &now
	task.CompletedAt = &now
	task.Stdout = "Assembly complete"
	exitCode := 0
	task.ExitCode = &exitCode

	if err := st.UpdateTask(ctx, task); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := st.GetTask(ctx, task.ID)
	if got.State != model.TaskStateSuccess {
		t.Errorf("state = %q, want SUCCESS", got.State)
	}
	if got.Stdout != "Assembly complete" {
		t.Errorf("stdout = %q, want Assembly complete", got.Stdout)
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", got.ExitCode)
	}
}

func TestGetTasksByState(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create PENDING task.
	task1 := sampleTask(sub.ID)
	st.CreateTask(ctx, task1)

	// Create RUNNING task.
	task2 := sampleTask(sub.ID)
	task2.ID = "task_test-2"
	task2.State = model.TaskStateRunning
	st.CreateTask(ctx, task2)

	pending, err := st.GetTasksByState(ctx, model.TaskStatePending)
	if err != nil {
		t.Fatalf("get by state: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("pending count = %d, want 1", len(pending))
	}
	if pending[0].ID != task1.ID {
		t.Errorf("pending task = %q, want %q", pending[0].ID, task1.ID)
	}
}

// --- GetSubmission loads tasks ---

func TestGetSubmission_LoadsTasks(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)
	task := sampleTask(sub.ID)
	st.CreateTask(ctx, task)

	got, err := st.GetSubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Tasks) != 1 {
		t.Errorf("tasks count = %d, want 1", len(got.Tasks))
	}
	if got.Tasks[0].ID != task.ID {
		t.Errorf("task id = %q, want %q", got.Tasks[0].ID, task.ID)
	}
}

// --- Worker tests ---

func sampleWorker() *model.Worker {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return &model.Worker{
		ID:           "wrk_test-1",
		Name:         "test-worker",
		Hostname:     "localhost",
		State:        model.WorkerStateOnline,
		Runtime:      model.RuntimeNone,
		Labels:       map[string]string{"env": "test"},
		LastSeen:     now,
		RegisteredAt: now,
	}
}

func TestCreateAndGetWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()

	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("got nil worker")
	}
	if got.ID != w.ID {
		t.Errorf("id = %q, want %q", got.ID, w.ID)
	}
	if got.Name != w.Name {
		t.Errorf("name = %q, want %q", got.Name, w.Name)
	}
	if got.State != model.WorkerStateOnline {
		t.Errorf("state = %q, want online", got.State)
	}
	if got.Runtime != model.RuntimeNone {
		t.Errorf("runtime = %q, want none", got.Runtime)
	}
	if got.Labels["env"] != "test" {
		t.Errorf("labels = %v, want env=test", got.Labels)
	}
}

func TestGetWorker_NotFound(t *testing.T) {
	st := testStore(t)
	got, err := st.GetWorker(context.Background(), "wrk_nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestUpdateWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	st.CreateWorker(ctx, w)

	w.State = model.WorkerStateDraining
	w.CurrentTask = "task_123"
	if err := st.UpdateWorker(ctx, w); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := st.GetWorker(ctx, w.ID)
	if got.State != model.WorkerStateDraining {
		t.Errorf("state = %q, want draining", got.State)
	}
	if got.CurrentTask != "task_123" {
		t.Errorf("current_task = %q, want task_123", got.CurrentTask)
	}
}

func TestDeleteWorker(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	w := sampleWorker()
	st.CreateWorker(ctx, w)

	if err := st.DeleteWorker(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ := st.GetWorker(ctx, w.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteWorker_NotFound(t *testing.T) {
	st := testStore(t)
	if err := st.DeleteWorker(context.Background(), "wrk_nonexistent"); err == nil {
		t.Error("expected error for nonexistent worker")
	}
}

func TestListWorkers(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	w1 := sampleWorker()
	st.CreateWorker(ctx, w1)

	w2 := sampleWorker()
	w2.ID = "wrk_test-2"
	w2.Name = "worker-2"
	w2.Runtime = model.RuntimeDocker
	w2.RegisteredAt = w1.RegisteredAt.Add(time.Second)
	st.CreateWorker(ctx, w2)

	workers, err := st.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(workers) != 2 {
		t.Errorf("len = %d, want 2", len(workers))
	}
}

func TestCheckoutTask_BasicFlow(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Set up workflow + submission.
	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create a QUEUED worker task.
	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	st.CreateTask(ctx, task)

	// Create a worker.
	w := sampleWorker()
	st.CreateWorker(ctx, w)

	// Checkout should return the task.
	got, err := st.CheckoutTask(ctx, w.ID, model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got == nil {
		t.Fatal("expected a task, got nil")
	}
	if got.ID != task.ID {
		t.Errorf("task id = %q, want %q", got.ID, task.ID)
	}
	if got.State != model.TaskStateRunning {
		t.Errorf("state = %q, want RUNNING", got.State)
	}
	if got.ExternalID != w.ID {
		t.Errorf("external_id = %q, want %q", got.ExternalID, w.ID)
	}

	// Second checkout should return nil (no more tasks).
	got2, err := st.CheckoutTask(ctx, w.ID, model.RuntimeNone)
	if err != nil {
		t.Fatalf("second checkout: %v", err)
	}
	if got2 != nil {
		t.Errorf("expected nil on second checkout, got task %s", got2.ID)
	}
}

func TestCheckoutTask_RuntimeFiltering(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create a QUEUED task that requires Docker (_docker_image present).
	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	task.Inputs = map[string]any{
		"_base_command": []any{"echo", "hello"},
		"_docker_image": "alpine:latest",
	}
	st.CreateTask(ctx, task)

	w := sampleWorker()
	st.CreateWorker(ctx, w)

	// Worker with runtime=none should NOT get this task.
	got, err := st.CheckoutTask(ctx, w.ID, model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got != nil {
		t.Errorf("bare worker should not get Docker task, got %s", got.ID)
	}

	// Worker with runtime=docker SHOULD get this task.
	got2, err := st.CheckoutTask(ctx, w.ID, model.RuntimeDocker)
	if err != nil {
		t.Fatalf("checkout docker: %v", err)
	}
	if got2 == nil {
		t.Fatal("docker worker should get the task")
	}
	if got2.ID != task.ID {
		t.Errorf("task id = %q, want %q", got2.ID, task.ID)
	}
}

func TestCheckoutTask_NoQueuedTasks(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	w := sampleWorker()
	st.CreateWorker(ctx, w)

	got, err := st.CheckoutTask(ctx, w.ID, model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no tasks, got %s", got.ID)
	}
}
