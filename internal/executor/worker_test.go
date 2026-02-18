package executor

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// mockTaskStore implements TaskReader for testing WorkerExecutor.
type mockTaskStore struct {
	tasks map[string]*model.Task
}

func newMockTaskStore() *mockTaskStore {
	return &mockTaskStore{tasks: make(map[string]*model.Task)}
}

func (s *mockTaskStore) GetTask(_ context.Context, id string) (*model.Task, error) {
	t, ok := s.tasks[id]
	if !ok {
		return nil, nil
	}
	// Return a copy to simulate database read.
	copy := *t
	return &copy, nil
}

func (s *mockTaskStore) UpdateTask(_ context.Context, task *model.Task) error {
	s.tasks[task.ID] = task
	return nil
}

func TestWorkerExecutor_Type(t *testing.T) {
	e := NewWorkerExecutor(newMockTaskStore(), newTestLogger())
	if got := e.Type(); got != model.ExecutorTypeWorker {
		t.Fatalf("Type() = %q, want %q", got, model.ExecutorTypeWorker)
	}
}

func TestWorkerExecutor_Submit(t *testing.T) {
	e := NewWorkerExecutor(newMockTaskStore(), newTestLogger())

	task := &model.Task{
		ID:    "task_worker_1",
		State: model.TaskStateScheduled,
	}

	externalID, err := e.Submit(context.Background(), task)
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if externalID != "task_worker_1" {
		t.Errorf("externalID = %q, want task_worker_1", externalID)
	}
}

func TestWorkerExecutor_Status_ReadsFromStore(t *testing.T) {
	store := newMockTaskStore()
	e := NewWorkerExecutor(store, newTestLogger())

	// Simulate a task that a worker has completed.
	exitCode := 0
	now := time.Now().UTC()
	store.tasks["task_w1"] = &model.Task{
		ID:          "task_w1",
		State:       model.TaskStateSuccess,
		Stdout:      "hello world",
		Stderr:      "",
		ExitCode:    &exitCode,
		Outputs:     map[string]any{"result": "file:///tmp/out.txt"},
		CompletedAt: &now,
	}

	// Caller has stale task.
	callerTask := &model.Task{
		ID:    "task_w1",
		State: model.TaskStateRunning,
	}

	state, err := e.Status(context.Background(), callerTask)
	if err != nil {
		t.Fatalf("Status error: %v", err)
	}
	if state != model.TaskStateSuccess {
		t.Errorf("state = %q, want SUCCESS", state)
	}
	// Caller task should be updated with fresh data.
	if callerTask.Stdout != "hello world" {
		t.Errorf("stdout = %q, want hello world", callerTask.Stdout)
	}
	if callerTask.ExitCode == nil || *callerTask.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", callerTask.ExitCode)
	}
}

func TestWorkerExecutor_Status_NotFound(t *testing.T) {
	store := newMockTaskStore()
	e := NewWorkerExecutor(store, newTestLogger())

	task := &model.Task{ID: "task_missing"}
	_, err := e.Status(context.Background(), task)
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestWorkerExecutor_Cancel(t *testing.T) {
	store := newMockTaskStore()
	e := NewWorkerExecutor(store, newTestLogger())

	store.tasks["task_c1"] = &model.Task{
		ID:    "task_c1",
		State: model.TaskStateRunning,
	}

	task := &model.Task{ID: "task_c1"}
	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("Cancel error: %v", err)
	}

	// Task should now be FAILED.
	if store.tasks["task_c1"].State != model.TaskStateFailed {
		t.Errorf("state = %q, want FAILED", store.tasks["task_c1"].State)
	}
	if store.tasks["task_c1"].CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestWorkerExecutor_Cancel_AlreadyTerminal(t *testing.T) {
	store := newMockTaskStore()
	e := NewWorkerExecutor(store, newTestLogger())

	store.tasks["task_c2"] = &model.Task{
		ID:    "task_c2",
		State: model.TaskStateSuccess,
	}

	task := &model.Task{ID: "task_c2"}
	err := e.Cancel(context.Background(), task)
	if err != nil {
		t.Fatalf("Cancel error: %v", err)
	}

	// Should remain SUCCESS.
	if store.tasks["task_c2"].State != model.TaskStateSuccess {
		t.Errorf("state = %q, want SUCCESS (unchanged)", store.tasks["task_c2"].State)
	}
}

func TestWorkerExecutor_Logs(t *testing.T) {
	store := newMockTaskStore()
	e := NewWorkerExecutor(store, newTestLogger())

	store.tasks["task_l1"] = &model.Task{
		ID:     "task_l1",
		Stdout: "out data",
		Stderr: "err data",
	}

	task := &model.Task{ID: "task_l1"}
	stdout, stderr, err := e.Logs(context.Background(), task)
	if err != nil {
		t.Fatalf("Logs error: %v", err)
	}
	if stdout != "out data" {
		t.Errorf("stdout = %q, want out data", stdout)
	}
	if stderr != "err data" {
		t.Errorf("stderr = %q, want err data", stderr)
	}
}
