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

// --- Workflow Labels tests ---

func TestWorkflowLabels_CreateAndGet(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	wf.Labels = map[string]string{"domain": "genomics", "org": "bvbrc"}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetWorkflow(ctx, wf.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Labels) != 2 {
		t.Fatalf("labels count = %d, want 2", len(got.Labels))
	}
	if got.Labels["domain"] != "genomics" {
		t.Errorf("labels[domain] = %q, want genomics", got.Labels["domain"])
	}
	if got.Labels["org"] != "bvbrc" {
		t.Errorf("labels[org] = %q, want bvbrc", got.Labels["org"])
	}
}

func TestWorkflowLabels_Filter(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Create workflows with different labels.
	wf1 := sampleWorkflow()
	wf1.ID = "wf_labeled-1"
	wf1.Labels = map[string]string{"domain": "genomics"}
	if err := st.CreateWorkflow(ctx, wf1); err != nil {
		t.Fatalf("create %s: %v", wf1.ID, err)
	}

	wf2 := sampleWorkflow()
	wf2.ID = "wf_labeled-2"
	wf2.Name = "wf2"
	wf2.Labels = map[string]string{"domain": "proteomics"}
	if err := st.CreateWorkflow(ctx, wf2); err != nil {
		t.Fatalf("create %s: %v", wf2.ID, err)
	}

	wf3 := sampleWorkflow()
	wf3.ID = "wf_labeled-3"
	wf3.Name = "wf3"
	wf3.Labels = map[string]string{"domain": "genomics", "org": "bvbrc"}
	if err := st.CreateWorkflow(ctx, wf3); err != nil {
		t.Fatalf("create %s: %v", wf3.ID, err)
	}

	// Filter by key:value
	opts := model.DefaultListOptions()
	opts.Labels = []string{"domain:genomics"}
	results, total, err := st.ListWorkflows(ctx, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(results) != 2 {
		t.Errorf("results = %d, want 2", len(results))
	}

	// Filter by value only (any key)
	opts.Labels = []string{"bvbrc"}
	results, total, err = st.ListWorkflows(ctx, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}

	// Multi-label AND filter
	opts.Labels = []string{"domain:genomics", "org:bvbrc"}
	results, total, err = st.ListWorkflows(ctx, opts)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1 (AND filter)", total)
	}
	if len(results) != 1 || results[0].ID != "wf_labeled-3" {
		t.Errorf("expected wf_labeled-3, got %v", results)
	}
}

func TestWorkflowLabels_UpdatePreservesLabels(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	wf.Labels = map[string]string{"domain": "genomics"}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Update labels
	wf.Labels = map[string]string{"domain": "proteomics", "status": "active"}
	wf.UpdatedAt = time.Now().UTC()
	if err := st.UpdateWorkflow(ctx, wf); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := st.GetWorkflow(ctx, wf.ID)
	if got.Labels["domain"] != "proteomics" {
		t.Errorf("labels[domain] = %q, want proteomics", got.Labels["domain"])
	}
	if got.Labels["status"] != "active" {
		t.Errorf("labels[status] = %q, want active", got.Labels["status"])
	}
}

func TestWorkflowLabels_EmptyLabels(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	// No labels set (nil)
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, _ := st.GetWorkflow(ctx, wf.ID)
	if got.Labels == nil {
		// nil is acceptable; empty map is also OK
	} else if len(got.Labels) != 0 {
		t.Errorf("expected empty or nil labels, got %v", got.Labels)
	}
}

// --- Label Vocabulary tests ---

func TestLabelVocabulary_CRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	lv := &model.LabelVocabulary{
		ID:          "lv_test-1",
		Key:         "domain",
		Value:       "genomics",
		Description: "Genomics workflows",
		Color:       "blue",
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}

	if err := st.CreateLabelVocabulary(ctx, lv); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Create a second entry.
	lv2 := &model.LabelVocabulary{
		ID:        "lv_test-2",
		Key:       "domain",
		Value:     "proteomics",
		Color:     "green",
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := st.CreateLabelVocabulary(ctx, lv2); err != nil {
		t.Fatalf("create 2: %v", err)
	}

	// List all.
	entries, err := st.ListLabelVocabulary(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("list count = %d, want 2", len(entries))
	}
	if entries[0].Value != "genomics" || entries[1].Value != "proteomics" {
		t.Errorf("unexpected order: %v, %v", entries[0].Value, entries[1].Value)
	}

	// Delete one.
	if err := st.DeleteLabelVocabulary(ctx, "lv_test-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	entries, _ = st.ListLabelVocabulary(ctx)
	if len(entries) != 1 {
		t.Errorf("list after delete = %d, want 1", len(entries))
	}

	// Delete nonexistent.
	if err := st.DeleteLabelVocabulary(ctx, "lv_nonexistent"); err == nil {
		t.Error("expected error for nonexistent delete")
	}
}

func TestLabelVocabulary_UniqueConstraint(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	lv := &model.LabelVocabulary{
		ID:        "lv_dup-1",
		Key:       "domain",
		Value:     "genomics",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateLabelVocabulary(ctx, lv); err != nil {
		t.Fatalf("create initial: %v", err)
	}

	// Duplicate key:value should fail.
	lv2 := &model.LabelVocabulary{
		ID:        "lv_dup-2",
		Key:       "domain",
		Value:     "genomics",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateLabelVocabulary(ctx, lv2); err == nil {
		t.Error("expected error for duplicate key:value")
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

func TestListTasksBySubmissionPaged(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create three tasks with different states and times.
	task1 := sampleTask(sub.ID)
	task1.State = model.TaskStatePending
	st.CreateTask(ctx, task1)

	task2 := sampleTask(sub.ID)
	task2.ID = "task_test-2"
	task2.StepID = "annotate"
	task2.State = model.TaskStateSuccess
	task2.CreatedAt = task1.CreatedAt.Add(time.Second)
	st.CreateTask(ctx, task2)

	task3 := sampleTask(sub.ID)
	task3.ID = "task_test-3"
	task3.StepID = "report"
	task3.State = model.TaskStatePending
	task3.CreatedAt = task1.CreatedAt.Add(2 * time.Second)
	st.CreateTask(ctx, task3)

	t.Run("all tasks", func(t *testing.T) {
		tasks, total, err := st.ListTasksBySubmissionPaged(ctx, sub.ID, model.DefaultListOptions())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		if len(tasks) != 3 {
			t.Errorf("len = %d, want 3", len(tasks))
		}
	})

	t.Run("state filter", func(t *testing.T) {
		opts := model.DefaultListOptions()
		opts.State = string(model.TaskStatePending)
		tasks, total, err := st.ListTasksBySubmissionPaged(ctx, sub.ID, opts)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 2 {
			t.Errorf("total = %d, want 2", total)
		}
		if len(tasks) != 2 {
			t.Errorf("len = %d, want 2", len(tasks))
		}
	})

	t.Run("pagination", func(t *testing.T) {
		opts := model.ListOptions{Limit: 2, Offset: 0}
		tasks, total, err := st.ListTasksBySubmissionPaged(ctx, sub.ID, opts)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		if len(tasks) != 2 {
			t.Errorf("page len = %d, want 2", len(tasks))
		}

		// Second page.
		opts.Offset = 2
		tasks, total, err = st.ListTasksBySubmissionPaged(ctx, sub.ID, opts)
		if err != nil {
			t.Fatalf("list page 2: %v", err)
		}
		if total != 3 {
			t.Errorf("total = %d, want 3", total)
		}
		if len(tasks) != 1 {
			t.Errorf("page 2 len = %d, want 1", len(tasks))
		}
	})

	t.Run("sort by step_id asc", func(t *testing.T) {
		opts := model.DefaultListOptions()
		opts.SortBy = "step_id"
		opts.SortDir = "asc"
		tasks, _, err := st.ListTasksBySubmissionPaged(ctx, sub.ID, opts)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(tasks) != 3 {
			t.Fatalf("len = %d, want 3", len(tasks))
		}
		if tasks[0].StepID != "annotate" {
			t.Errorf("first step = %q, want annotate", tasks[0].StepID)
		}
		if tasks[2].StepID != "report" {
			t.Errorf("last step = %q, want report", tasks[2].StepID)
		}
	})
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

func TestMarkStaleWorkersOffline(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Create two workers: one recent, one stale.
	recent := sampleWorker()
	recent.LastSeen = time.Now().UTC()
	st.CreateWorker(ctx, recent)

	stale := sampleWorker()
	stale.ID = "wrk_stale-1"
	stale.Name = "stale-worker"
	stale.LastSeen = time.Now().UTC().Add(-5 * time.Minute)
	st.CreateWorker(ctx, stale)

	// Mark workers offline with 1-minute timeout.
	marked, err := st.MarkStaleWorkersOffline(ctx, 1*time.Minute)
	if err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	if len(marked) != 1 {
		t.Fatalf("marked = %d, want 1", len(marked))
	}
	if marked[0].ID != stale.ID {
		t.Errorf("marked worker = %s, want %s", marked[0].ID, stale.ID)
	}

	// Verify the stale worker is offline.
	got, _ := st.GetWorker(ctx, stale.ID)
	if got.State != model.WorkerStateOffline {
		t.Errorf("stale worker state = %s, want offline", got.State)
	}

	// Verify the recent worker is still online.
	got, _ = st.GetWorker(ctx, recent.ID)
	if got.State != model.WorkerStateOnline {
		t.Errorf("recent worker state = %s, want online", got.State)
	}
}

func TestRequeueWorkerTasks(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Create a RUNNING task assigned to a worker.
	task := sampleTask(sub.ID)
	task.State = model.TaskStateRunning
	task.ExecutorType = model.ExecutorTypeWorker
	task.ExternalID = "wrk_dead-1"
	now := time.Now().UTC()
	task.StartedAt = &now
	st.CreateTask(ctx, task)

	// Requeue the dead worker's tasks.
	n, err := st.RequeueWorkerTasks(ctx, "wrk_dead-1")
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if n != 1 {
		t.Errorf("requeued = %d, want 1", n)
	}

	// Task should be back to QUEUED.
	got, _ := st.GetTask(ctx, task.ID)
	if got.State != model.TaskStateQueued {
		t.Errorf("task state = %s, want QUEUED", got.State)
	}
	if got.ExternalID != "" {
		t.Errorf("external_id = %q, want empty", got.ExternalID)
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
	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
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
	got2, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
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
	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got != nil {
		t.Errorf("bare worker should not get Docker task, got %s", got.ID)
	}

	// Worker with runtime=docker SHOULD get this task.
	got2, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeDocker)
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

	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no tasks, got %s", got.ID)
	}
}

func TestCheckoutTask_PrestageDatasetRequired(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Task requires prestage dataset "boltz".
	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	task.RuntimeHints = &model.RuntimeHints{
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "boltz", Path: "/data/boltz", Mode: "prestage"},
		},
	}
	st.CreateTask(ctx, task)

	// Worker WITHOUT the dataset should NOT get the task.
	w := sampleWorker()
	w.Datasets = map[string]string{}
	st.CreateWorker(ctx, w)

	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got != nil {
		t.Errorf("worker without prestage dataset should not get task, got %s", got.ID)
	}

	// Worker WITH the dataset should get the task.
	w2 := sampleWorker()
	w2.ID = "wrk_test-2"
	w2.Datasets = map[string]string{"boltz": "/data/boltz"}
	st.CreateWorker(ctx, w2)

	got2, err := st.CheckoutTask(ctx, w2.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got2 == nil {
		t.Fatal("worker with prestage dataset should get the task")
	}
	if got2.ID != task.ID {
		t.Errorf("task id = %q, want %q", got2.ID, task.ID)
	}
}

func TestCheckoutTask_CacheDatasetPreference(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	// Task with cache dataset "boltz" — soft preference, not hard require.
	task := sampleTask(sub.ID)
	task.State = model.TaskStateQueued
	task.ExecutorType = model.ExecutorTypeWorker
	task.RuntimeHints = &model.RuntimeHints{
		RequiredDatasets: []model.DatasetRequirement{
			{ID: "boltz", Path: "/data/boltz", Mode: "cache"},
		},
	}
	st.CreateTask(ctx, task)

	// Worker WITHOUT the dataset should still get the task (cache is soft).
	w := sampleWorker()
	w.Datasets = map[string]string{}
	st.CreateWorker(ctx, w)

	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if got == nil {
		t.Fatal("worker without cache dataset should still get the task")
	}
	if got.ID != task.ID {
		t.Errorf("task id = %q, want %q", got.ID, task.ID)
	}
}

// --- BatchCreateStepInstances tests ---

func TestBatchCreateStepInstances(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	now := time.Now().UTC().Truncate(time.Millisecond)
	steps := []*model.StepInstance{
		{
			ID:            "si_batch-1",
			SubmissionID:  sub.ID,
			StepID:        "step1",
			State:         model.StepStateWaiting,
			ScatterCount:  2,
			ScatterMethod: "dotproduct",
			ScatterDims:   []int{3, 4},
			Outputs:       map[string]any{"out": "val1"},
			CreatedAt:     now,
		},
		{
			ID:           "si_batch-2",
			SubmissionID: sub.ID,
			StepID:       "step2",
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		},
		{
			ID:           "si_batch-3",
			SubmissionID: sub.ID,
			StepID:       "step3",
			State:        model.StepStateReady,
			Outputs:      map[string]any{"x": 42.0},
			CreatedAt:    now,
		},
	}

	if err := st.BatchCreateStepInstances(ctx, steps); err != nil {
		t.Fatalf("batch create: %v", err)
	}

	// Verify all rows were created.
	got, err := st.ListStepsBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("row count = %d, want 3", len(got))
	}

	// Verify fields persisted correctly on the first step.
	var si1 *model.StepInstance
	for _, si := range got {
		if si.ID == "si_batch-1" {
			si1 = si
			break
		}
	}
	if si1 == nil {
		t.Fatal("si_batch-1 not found")
	}
	if si1.StepID != "step1" {
		t.Errorf("step_id = %q, want step1", si1.StepID)
	}
	if si1.State != model.StepStateWaiting {
		t.Errorf("state = %q, want WAITING", si1.State)
	}
	if si1.ScatterCount != 2 {
		t.Errorf("scatter_count = %d, want 2", si1.ScatterCount)
	}
	if si1.ScatterMethod != "dotproduct" {
		t.Errorf("scatter_method = %q, want dotproduct", si1.ScatterMethod)
	}
	if len(si1.ScatterDims) != 2 || si1.ScatterDims[0] != 3 || si1.ScatterDims[1] != 4 {
		t.Errorf("scatter_dims = %v, want [3,4]", si1.ScatterDims)
	}
	if si1.Outputs["out"] != "val1" {
		t.Errorf("outputs = %v, want {out:val1}", si1.Outputs)
	}
}

func TestBatchCreateStepInstances_Empty(t *testing.T) {
	st := testStore(t)
	if err := st.BatchCreateStepInstances(context.Background(), nil); err != nil {
		t.Fatalf("empty batch should not error: %v", err)
	}
}

// --- CancelNonTerminalSteps tests ---

func TestCancelNonTerminalSteps(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	now := time.Now().UTC().Truncate(time.Millisecond)
	cancelTime := now.Add(time.Minute)

	// Create steps in various states.
	allStates := []struct {
		id    string
		state model.StepInstanceState
	}{
		{"si_waiting", model.StepStateWaiting},
		{"si_ready", model.StepStateReady},
		{"si_dispatched", model.StepStateDispatched},
		{"si_running", model.StepStateRunning},
		{"si_completed", model.StepStateCompleted},
		{"si_failed", model.StepStateFailed},
		{"si_skipped", model.StepStateSkipped},
	}

	for _, s := range allStates {
		si := &model.StepInstance{
			ID:           s.id,
			SubmissionID: sub.ID,
			StepID:       "step1",
			State:        s.state,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	cancelled, err := st.CancelNonTerminalSteps(ctx, sub.ID, cancelTime)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// 4 non-terminal (WAITING, READY, DISPATCHED, RUNNING) should be cancelled.
	if cancelled != 4 {
		t.Errorf("cancelled = %d, want 4", cancelled)
	}

	// Verify terminal states are preserved.
	steps, err := st.ListStepsBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	stateMap := map[string]model.StepInstanceState{}
	for _, si := range steps {
		stateMap[si.ID] = si.State
	}

	// Non-terminal should be SKIPPED now.
	for _, id := range []string{"si_waiting", "si_ready", "si_dispatched", "si_running"} {
		if stateMap[id] != model.StepStateSkipped {
			t.Errorf("%s state = %q, want SKIPPED", id, stateMap[id])
		}
	}
	// Terminal states should be unchanged.
	if stateMap["si_completed"] != model.StepStateCompleted {
		t.Errorf("si_completed state = %q, want COMPLETED", stateMap["si_completed"])
	}
	if stateMap["si_failed"] != model.StepStateFailed {
		t.Errorf("si_failed state = %q, want FAILED", stateMap["si_failed"])
	}
	if stateMap["si_skipped"] != model.StepStateSkipped {
		t.Errorf("si_skipped state = %q, want SKIPPED", stateMap["si_skipped"])
	}
}

// --- CancelNonTerminalTasks tests ---

func TestCancelNonTerminalTasks(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	st.CreateWorkflow(ctx, wf)
	sub := sampleSubmission(wf.ID)
	st.CreateSubmission(ctx, sub)

	now := time.Now().UTC().Truncate(time.Millisecond)
	cancelTime := now.Add(time.Minute)

	// Create tasks in various states.
	allStates := []struct {
		id    string
		state model.TaskState
	}{
		{"task_pending", model.TaskStatePending},
		{"task_scheduled", model.TaskStateScheduled},
		{"task_queued", model.TaskStateQueued},
		{"task_running", model.TaskStateRunning},
		{"task_success", model.TaskStateSuccess},
		{"task_failed", model.TaskStateFailed},
		{"task_skipped", model.TaskStateSkipped},
	}

	for _, s := range allStates {
		task := sampleTask(sub.ID)
		task.ID = s.id
		task.State = s.state
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %s: %v", s.id, err)
		}
	}

	cancelled, err := st.CancelNonTerminalTasks(ctx, sub.ID, cancelTime)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}

	// 4 non-terminal (PENDING, SCHEDULED, QUEUED, RUNNING) should be cancelled.
	if cancelled != 4 {
		t.Errorf("cancelled = %d, want 4", cancelled)
	}

	// Verify terminal states are preserved.
	tasks, err := st.ListTasksBySubmission(ctx, sub.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	stateMap := map[string]model.TaskState{}
	for _, task := range tasks {
		stateMap[task.ID] = task.State
	}

	// Non-terminal should be SKIPPED now.
	for _, id := range []string{"task_pending", "task_scheduled", "task_queued", "task_running"} {
		if stateMap[id] != model.TaskStateSkipped {
			t.Errorf("%s state = %q, want SKIPPED", id, stateMap[id])
		}
	}
	// Terminal states should be unchanged.
	if stateMap["task_success"] != model.TaskStateSuccess {
		t.Errorf("task_success state = %q, want SUCCESS", stateMap["task_success"])
	}
	if stateMap["task_failed"] != model.TaskStateFailed {
		t.Errorf("task_failed state = %q, want FAILED", stateMap["task_failed"])
	}
	if stateMap["task_skipped"] != model.TaskStateSkipped {
		t.Errorf("task_skipped state = %q, want SKIPPED", stateMap["task_skipped"])
	}
}
