package scheduler

import (
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestResolveTaskInputs_WorkflowInput(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "greet"}
	step := &model.Step{
		ID: "greet",
		In: []model.StepInput{
			{ID: "message", Source: "msg"},
		},
	}
	subInputs := map[string]any{"msg": "hello"}

	if err := ResolveTaskInputs(task, step, subInputs, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := task.Inputs["message"]; got != "hello" {
		t.Errorf("Inputs[\"message\"] = %v, want \"hello\"", got)
	}
}

func TestResolveTaskInputs_UpstreamOutput(t *testing.T) {
	upstream := &model.Task{
		ID:     "task_0",
		StepID: "step1",
		State:  model.TaskStateSuccess,
		Outputs: map[string]any{
			"contigs": "/path/to/file",
		},
	}
	tasksByStepID := map[string]*model.Task{"step1": upstream}

	task := &model.Task{ID: "task_1", StepID: "step2"}
	step := &model.Step{
		ID: "step2",
		In: []model.StepInput{
			{ID: "input_file", Source: "step1/contigs"},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, tasksByStepID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := task.Inputs["input_file"]; got != "/path/to/file" {
		t.Errorf("Inputs[\"input_file\"] = %v, want \"/path/to/file\"", got)
	}
}

func TestResolveTaskInputs_BaseCommandAndGlobs(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "echo_step"}
	step := &model.Step{
		ID: "echo_step",
		ToolInline: &model.Tool{
			ID:          "echo_tool",
			Class:       "CommandLineTool",
			BaseCommand: []string{"echo", "test"},
			Outputs: []model.ToolOutput{
				{ID: "out1", Type: "File", Glob: "*.txt"},
				{ID: "out2", Type: "File", Glob: ""},
				{ID: "out3", Type: "File", Glob: "results/*.csv"},
			},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check _base_command
	cmd, ok := task.Inputs["_base_command"]
	if !ok {
		t.Fatal("Inputs[\"_base_command\"] not set")
	}
	cmdSlice, ok := cmd.([]any)
	if !ok {
		t.Fatalf("_base_command is %T, want []any", cmd)
	}
	if len(cmdSlice) != 2 || cmdSlice[0] != "echo" || cmdSlice[1] != "test" {
		t.Errorf("_base_command = %v, want [echo test]", cmdSlice)
	}

	// Check _output_globs
	globs, ok := task.Inputs["_output_globs"]
	if !ok {
		t.Fatal("Inputs[\"_output_globs\"] not set")
	}
	globsMap, ok := globs.(map[string]any)
	if !ok {
		t.Fatalf("_output_globs is %T, want map[string]any", globs)
	}
	if globsMap["out1"] != "*.txt" {
		t.Errorf("_output_globs[\"out1\"] = %v, want \"*.txt\"", globsMap["out1"])
	}
	if _, exists := globsMap["out2"]; exists {
		t.Error("_output_globs should not contain out2 (empty glob)")
	}
	if globsMap["out3"] != "results/*.csv" {
		t.Errorf("_output_globs[\"out3\"] = %v, want \"results/*.csv\"", globsMap["out3"])
	}
}

func TestResolveTaskInputs_DockerImage(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "docker_step"}
	step := &model.Step{
		ID: "docker_step",
		Hints: &model.StepHints{
			ExecutorType: model.ExecutorTypeContainer,
			DockerImage:  "ubuntu:22.04",
		},
		ToolInline: &model.Tool{
			ID:          "docker_tool",
			Class:       "CommandLineTool",
			BaseCommand: []string{"echo", "hello"},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	img, ok := task.Inputs["_docker_image"]
	if !ok {
		t.Fatal("_docker_image not set in task.Inputs")
	}
	if img != "ubuntu:22.04" {
		t.Errorf("_docker_image = %v, want ubuntu:22.04", img)
	}

	// Verify _base_command is also set.
	if _, ok := task.Inputs["_base_command"]; !ok {
		t.Error("_base_command not set in task.Inputs")
	}
}

func TestResolveTaskInputs_NoDockerImageWithoutHints(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		ToolInline: &model.Tool{
			ID:          "tool1",
			Class:       "CommandLineTool",
			BaseCommand: []string{"echo"},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := task.Inputs["_docker_image"]; ok {
		t.Error("_docker_image should not be set when no hints are present")
	}
}

func TestResolveTaskInputs_MissingWorkflowInput(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "param", Source: "missing_input"},
		},
	}

	err := ResolveTaskInputs(task, step, map[string]any{}, nil)
	if err == nil {
		t.Fatal("expected error for missing workflow input, got nil")
	}
}

func TestResolveTaskInputs_MissingUpstreamOutput(t *testing.T) {
	upstream := &model.Task{
		ID:      "task_0",
		StepID:  "step1",
		State:   model.TaskStateSuccess,
		Outputs: map[string]any{"existing_output": "value"},
	}
	tasksByStepID := map[string]*model.Task{"step1": upstream}

	task := &model.Task{ID: "task_1", StepID: "step2"}
	step := &model.Step{
		ID: "step2",
		In: []model.StepInput{
			{ID: "input_file", Source: "step1/missing_output"},
		},
	}

	err := ResolveTaskInputs(task, step, nil, tasksByStepID)
	if err == nil {
		t.Fatal("expected error for missing upstream output, got nil")
	}
}

func TestResolveTaskInputs_EmptySource(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "optional_param", Source: ""},
		},
	}

	if err := ResolveTaskInputs(task, step, map[string]any{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := task.Inputs["optional_param"]; exists {
		t.Error("Inputs should not contain key for empty-source input")
	}
}

func TestAreDependenciesSatisfied_NoDeps(t *testing.T) {
	task := &model.Task{ID: "task_1", DependsOn: nil}

	satisfied, blocked := AreDependenciesSatisfied(task, nil)
	if !satisfied || blocked {
		t.Errorf("got satisfied=%v, blocked=%v; want true, false", satisfied, blocked)
	}
}

func TestAreDependenciesSatisfied_AllSuccess(t *testing.T) {
	dep := &model.Task{ID: "task_0", StepID: "step1", State: model.TaskStateSuccess}
	tasksByStepID := map[string]*model.Task{"step1": dep}

	task := &model.Task{ID: "task_1", DependsOn: []string{"step1"}}

	satisfied, blocked := AreDependenciesSatisfied(task, tasksByStepID)
	if !satisfied || blocked {
		t.Errorf("got satisfied=%v, blocked=%v; want true, false", satisfied, blocked)
	}
}

func TestAreDependenciesSatisfied_StillPending(t *testing.T) {
	dep := &model.Task{ID: "task_0", StepID: "step1", State: model.TaskStatePending}
	tasksByStepID := map[string]*model.Task{"step1": dep}

	task := &model.Task{ID: "task_1", DependsOn: []string{"step1"}}

	satisfied, blocked := AreDependenciesSatisfied(task, tasksByStepID)
	if satisfied || blocked {
		t.Errorf("got satisfied=%v, blocked=%v; want false, false", satisfied, blocked)
	}
}

func TestAreDependenciesSatisfied_DepFailed(t *testing.T) {
	dep := &model.Task{ID: "task_0", StepID: "step1", State: model.TaskStateFailed}
	tasksByStepID := map[string]*model.Task{"step1": dep}

	task := &model.Task{ID: "task_1", DependsOn: []string{"step1"}}

	satisfied, blocked := AreDependenciesSatisfied(task, tasksByStepID)
	if satisfied || !blocked {
		t.Errorf("got satisfied=%v, blocked=%v; want false, true", satisfied, blocked)
	}
}

func TestAreDependenciesSatisfied_DepSkipped(t *testing.T) {
	dep := &model.Task{ID: "task_0", StepID: "step1", State: model.TaskStateSkipped}
	tasksByStepID := map[string]*model.Task{"step1": dep}

	task := &model.Task{ID: "task_1", DependsOn: []string{"step1"}}

	satisfied, blocked := AreDependenciesSatisfied(task, tasksByStepID)
	if satisfied || !blocked {
		t.Errorf("got satisfied=%v, blocked=%v; want false, true", satisfied, blocked)
	}
}

func TestResolveTaskInputs_BVBRCAppID(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "bvbrc_step"}
	step := &model.Step{
		ID: "bvbrc_step",
		Hints: &model.StepHints{
			BVBRCAppID:   "GenomeAssembly2",
			ExecutorType: model.ExecutorTypeBVBRC,
		},
	}

	if err := ResolveTaskInputs(task, step, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	appID, ok := task.Inputs["_bvbrc_app_id"]
	if !ok {
		t.Fatal("_bvbrc_app_id not set in task.Inputs")
	}
	if appID != "GenomeAssembly2" {
		t.Errorf("_bvbrc_app_id = %v, want GenomeAssembly2", appID)
	}
}

func TestResolveTaskInputs_NoBVBRCAppIDWithoutHints(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		ToolInline: &model.Tool{
			ID:          "tool1",
			Class:       "CommandLineTool",
			BaseCommand: []string{"echo"},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := task.Inputs["_bvbrc_app_id"]; ok {
		t.Error("_bvbrc_app_id should not be set when no hints are present")
	}
}

func TestBuildTasksByStepID(t *testing.T) {
	tasks := []*model.Task{
		{ID: "task_1", StepID: "step_a"},
		{ID: "task_2", StepID: "step_b"},
		{ID: "task_3", StepID: "step_c"},
	}

	m := BuildTasksByStepID(tasks)

	if len(m) != 3 {
		t.Fatalf("map length = %d, want 3", len(m))
	}
	for _, task := range tasks {
		got, ok := m[task.StepID]
		if !ok {
			t.Errorf("step %q not in map", task.StepID)
			continue
		}
		if got.ID != task.ID {
			t.Errorf("m[%q].ID = %q, want %q", task.StepID, got.ID, task.ID)
		}
	}
}
