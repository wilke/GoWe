package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
	"gopkg.in/yaml.v3"
)

// mockStore implements the minimal store.Store interface for testing.
type mockStore struct {
	workflows map[string]*model.Workflow // keyed by ID and name
}

func (m *mockStore) GetWorkflow(_ context.Context, id string) (*model.Workflow, error) {
	return m.workflows[id], nil
}

func (m *mockStore) GetWorkflowByName(_ context.Context, name string) (*model.Workflow, error) {
	return m.workflows[name], nil
}

func (m *mockStore) GetWorkflowByHash(_ context.Context, _ string) (*model.Workflow, error) {
	return nil, nil
}

// Stub the rest of the Store interface (unused in these tests).
func (m *mockStore) CreateWorkflow(context.Context, *model.Workflow) error  { return nil }
func (m *mockStore) ListWorkflows(context.Context, model.ListOptions) ([]*model.Workflow, int, error) {
	return nil, 0, nil
}
func (m *mockStore) UpdateWorkflow(context.Context, *model.Workflow) error { return nil }
func (m *mockStore) DeleteWorkflow(context.Context, string) error          { return nil }

func (m *mockStore) CreateSubmission(context.Context, *model.Submission) error  { return nil }
func (m *mockStore) GetSubmission(context.Context, string) (*model.Submission, error) {
	return nil, nil
}
func (m *mockStore) ListSubmissions(context.Context, model.ListOptions) ([]*model.Submission, int, error) {
	return nil, 0, nil
}
func (m *mockStore) UpdateSubmission(context.Context, *model.Submission) error { return nil }
func (m *mockStore) UpdateSubmissionInputs(context.Context, string, map[string]any) error {
	return nil
}
func (m *mockStore) GetChildSubmissions(context.Context, string) ([]*model.Submission, error) {
	return nil, nil
}
func (m *mockStore) CountSubmissionsByState(_ context.Context, _ time.Time) (map[string]int, error) {
	return nil, nil
}

func (m *mockStore) CreateStepInstance(context.Context, *model.StepInstance) error  { return nil }
func (m *mockStore) GetStepInstance(context.Context, string) (*model.StepInstance, error) {
	return nil, nil
}
func (m *mockStore) UpdateStepInstance(context.Context, *model.StepInstance) error { return nil }
func (m *mockStore) ListStepsBySubmission(context.Context, string) ([]*model.StepInstance, error) {
	return nil, nil
}
func (m *mockStore) ListStepsByState(context.Context, model.StepInstanceState) ([]*model.StepInstance, error) {
	return nil, nil
}

func (m *mockStore) CreateTask(context.Context, *model.Task) error           { return nil }
func (m *mockStore) GetTask(context.Context, string) (*model.Task, error)    { return nil, nil }
func (m *mockStore) ListTasksBySubmission(context.Context, string) ([]*model.Task, error) {
	return nil, nil
}
func (m *mockStore) ListTasksBySubmissionPaged(context.Context, string, model.ListOptions) ([]*model.Task, int, error) {
	return nil, 0, nil
}
func (m *mockStore) ListTasksByStepInstance(context.Context, string) ([]*model.Task, error) {
	return nil, nil
}
func (m *mockStore) UpdateTask(context.Context, *model.Task) error                         { return nil }
func (m *mockStore) GetTasksByState(context.Context, model.TaskState) ([]*model.Task, error) {
	return nil, nil
}

func (m *mockStore) CreateSession(context.Context, *model.Session) error            { return nil }
func (m *mockStore) GetSession(context.Context, string) (*model.Session, error)     { return nil, nil }
func (m *mockStore) DeleteSession(context.Context, string) error                    { return nil }
func (m *mockStore) DeleteExpiredSessions(context.Context) (int64, error)           { return 0, nil }
func (m *mockStore) DeleteSessionsByUserID(context.Context, string) (int64, error)  { return 0, nil }

func (m *mockStore) CreateWorker(context.Context, *model.Worker) error          { return nil }
func (m *mockStore) GetWorker(context.Context, string) (*model.Worker, error)   { return nil, nil }
func (m *mockStore) UpdateWorker(context.Context, *model.Worker) error          { return nil }
func (m *mockStore) DeleteWorker(context.Context, string) error                 { return nil }
func (m *mockStore) ListWorkers(context.Context) ([]*model.Worker, error)       { return nil, nil }
func (m *mockStore) CheckoutTask(context.Context, string, string, model.ContainerRuntime) (*model.Task, error) {
	return nil, nil
}
func (m *mockStore) MarkStaleWorkersOffline(_ context.Context, _ time.Duration) ([]*model.Worker, error) {
	return nil, nil
}
func (m *mockStore) RequeueWorkerTasks(context.Context, string) (int, error) { return 0, nil }

func (m *mockStore) GetUser(context.Context, string) (*model.User, error) { return nil, nil }
func (m *mockStore) GetOrCreateUser(context.Context, string, model.AuthProvider) (*model.User, error) {
	return nil, nil
}
func (m *mockStore) UpdateUser(context.Context, *model.User) error            { return nil }
func (m *mockStore) ListUsers(context.Context) ([]*model.User, error)         { return nil, nil }
func (m *mockStore) LinkProvider(context.Context, string, model.AuthProvider, string) error {
	return nil
}

func (m *mockStore) Close() error                      { return nil }
func (m *mockStore) Migrate(context.Context) error     { return nil }

func TestResolveGoweRefs_NoRefs(t *testing.T) {
	st := &mockStore{}
	cwl := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo`

	result, err := resolveGoweRefs(context.Background(), st, cwl)
	if err != nil {
		t.Fatal(err)
	}
	if result != cwl {
		t.Errorf("expected unchanged CWL, got:\n%s", result)
	}
}

func TestResolveGoweRefs_ByName(t *testing.T) {
	// A registered tool.
	toolCWL := `cwlVersion: v1.2
$graph:
  - id: tool
    class: CommandLineTool
    baseCommand: predict
    inputs:
      seq:
        type: File
    outputs:
      result:
        type: File
        outputBinding:
          glob: "*.pdb"
  - id: main
    class: Workflow
    inputs:
      seq:
        type: File
    outputs:
      result:
        type: File
        outputSource: run_tool/result
    steps:
      run_tool:
        run: "#tool"
        in:
          seq: seq
        out: [result]
`
	st := &mockStore{
		workflows: map[string]*model.Workflow{
			"predict-structure": {
				ID:     "wf_123",
				Name:   "predict-structure",
				RawCWL: toolCWL,
			},
		},
	}

	workflowCWL := `cwlVersion: v1.2
class: Workflow
inputs:
  seq:
    type: File
outputs:
  result:
    type: File
    outputSource: predict/result
steps:
  predict:
    run: gowe://predict-structure
    in:
      seq: seq
    out: [result]
`

	result, err := resolveGoweRefs(context.Background(), st, workflowCWL)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the result is a packed $graph document.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(result), &doc); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	graph, ok := doc["$graph"].([]any)
	if !ok {
		t.Fatal("expected $graph in result")
	}

	// Should have 2 entries: the resolved tool + the workflow.
	if len(graph) != 2 {
		t.Fatalf("expected 2 graph entries, got %d", len(graph))
	}

	// First entry should be the tool.
	toolEntry, ok := graph[0].(map[string]any)
	if !ok {
		t.Fatal("expected map for tool entry")
	}
	if toolEntry["class"] != "CommandLineTool" {
		t.Errorf("expected CommandLineTool, got %v", toolEntry["class"])
	}
	if toolEntry["id"] != "predict-structure" {
		t.Errorf("expected tool id 'predict-structure', got %v", toolEntry["id"])
	}

	// Workflow step should reference the tool by fragment.
	wfEntry, ok := graph[1].(map[string]any)
	if !ok {
		t.Fatal("expected map for workflow entry")
	}
	steps, _ := wfEntry["steps"].(map[string]any)
	predictStep, _ := steps["predict"].(map[string]any)
	runRef, _ := predictStep["run"].(string)
	if !strings.HasPrefix(runRef, "#") {
		t.Errorf("expected fragment reference, got %q", runRef)
	}
}

func TestResolveGoweRefs_ByID(t *testing.T) {
	toolCWL := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: compare
inputs:
  pdb:
    type: File
outputs:
  report:
    type: File
    outputBinding:
      glob: "*.json"
`
	st := &mockStore{
		workflows: map[string]*model.Workflow{
			"wf_abc123": {
				ID:     "wf_abc123",
				Name:   "protein-compare",
				RawCWL: toolCWL,
			},
		},
	}

	workflowCWL := `cwlVersion: v1.2
class: Workflow
inputs:
  pdb:
    type: File
outputs:
  report:
    type: File
    outputSource: compare/report
steps:
  compare:
    run: gowe://wf_abc123
    in:
      pdb: pdb
    out: [report]
`

	result, err := resolveGoweRefs(context.Background(), st, workflowCWL)
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(result), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	graph, ok := doc["$graph"].([]any)
	if !ok {
		t.Fatal("expected $graph")
	}
	if len(graph) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(graph))
	}

	// Tool should be the bare CommandLineTool (not wrapped).
	tool := graph[0].(map[string]any)
	if tool["class"] != "CommandLineTool" {
		t.Errorf("expected CommandLineTool, got %v", tool["class"])
	}
	if tool["id"] != "protein-compare" {
		t.Errorf("expected id 'protein-compare', got %v", tool["id"])
	}
}

func TestResolveGoweRefs_NotFound(t *testing.T) {
	st := &mockStore{workflows: map[string]*model.Workflow{}}

	workflowCWL := `cwlVersion: v1.2
class: Workflow
inputs: {}
outputs: {}
steps:
  step1:
    run: gowe://nonexistent
    in: {}
    out: []
`

	_, err := resolveGoweRefs(context.Background(), st, workflowCWL)
	if err == nil {
		t.Fatal("expected error for nonexistent reference")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestResolveGoweRefs_MultipleRefs(t *testing.T) {
	tool1CWL := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: predict
inputs:
  seq:
    type: File
outputs:
  pdb:
    type: File
    outputBinding:
      glob: "*.pdb"
`
	tool2CWL := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: compare
inputs:
  pdb:
    type: File
outputs:
  report:
    type: File
    outputBinding:
      glob: "*.json"
`
	st := &mockStore{
		workflows: map[string]*model.Workflow{
			"predict-structure": {ID: "wf_1", Name: "predict-structure", RawCWL: tool1CWL},
			"protein-compare":   {ID: "wf_2", Name: "protein-compare", RawCWL: tool2CWL},
		},
	}

	workflowCWL := `cwlVersion: v1.2
class: Workflow
inputs:
  seq:
    type: File
outputs:
  report:
    type: File
    outputSource: compare/report
steps:
  predict:
    run: gowe://predict-structure
    in:
      seq: seq
    out: [pdb]
  compare:
    run: gowe://protein-compare
    in:
      pdb: predict/pdb
    out: [report]
`

	result, err := resolveGoweRefs(context.Background(), st, workflowCWL)
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(result), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	graph, ok := doc["$graph"].([]any)
	if !ok {
		t.Fatal("expected $graph")
	}

	// 2 tools + 1 workflow = 3 entries.
	if len(graph) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(graph))
	}

	// Verify deterministic output: run 20 times and compare.
	for i := 0; i < 20; i++ {
		again, err := resolveGoweRefs(context.Background(), st, workflowCWL)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if again != result {
			t.Fatalf("iteration %d: non-deterministic output", i)
		}
	}
}
