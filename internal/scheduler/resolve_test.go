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

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
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

	if err := ResolveTaskInputs(task, step, nil, tasksByStepID, nil); err != nil {
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

	if err := ResolveTaskInputs(task, step, nil, nil, nil); err != nil {
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

	if err := ResolveTaskInputs(task, step, nil, nil, nil); err != nil {
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

	if err := ResolveTaskInputs(task, step, nil, nil, nil); err != nil {
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

	// Per CWL v1.2 semantics (see §4.1.5 "WorkflowStepInput"), a missing source
	// is treated as null rather than raising an error, so we expect nil here.
	err := ResolveTaskInputs(task, step, map[string]any{}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Inputs["param"] != nil {
		t.Errorf("expected nil for missing input, got %v", task.Inputs["param"])
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

	// Per CWL semantics, missing outputs resolve to nil (not an error).
	err := ResolveTaskInputs(task, step, nil, tasksByStepID, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.Inputs["input_file"] != nil {
		t.Errorf("expected nil for missing output, got %v", task.Inputs["input_file"])
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

	if err := ResolveTaskInputs(task, step, map[string]any{}, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Per CWL semantics, empty source resolves to nil and is included in the map.
	if task.Inputs["optional_param"] != nil {
		t.Errorf("expected nil for empty source, got %v", task.Inputs["optional_param"])
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

	if err := ResolveTaskInputs(task, step, nil, nil, nil); err != nil {
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

	if err := ResolveTaskInputs(task, step, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := task.Inputs["_bvbrc_app_id"]; ok {
		t.Error("_bvbrc_app_id should not be set when no hints are present")
	}
}

func TestResolveTaskInputs_MissingUpstreamOutput_SuccessEmptyOutputs(t *testing.T) {
	// BV-BRC tasks complete successfully but never populate Outputs.
	// The resolver should tolerate this (common trigger/dependency pattern).
	upstream := &model.Task{
		ID:      "task_0",
		StepID:  "get_date",
		State:   model.TaskStateSuccess,
		Outputs: map[string]any{}, // empty — typical for BV-BRC
	}
	tasksByStepID := map[string]*model.Task{"get_date": upstream}

	task := &model.Task{ID: "task_1", StepID: "wait"}
	step := &model.Step{
		ID: "wait",
		In: []model.StepInput{
			{ID: "trigger", Source: "get_date/date_result"},
			{ID: "sleep_time", Source: "sleep_seconds"},
		},
	}
	subInputs := map[string]any{"sleep_seconds": 5}

	if err := ResolveTaskInputs(task, step, subInputs, tasksByStepID, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// trigger should be nil (tolerated missing output)
	if val, exists := task.Inputs["trigger"]; !exists {
		t.Error("Inputs should contain key 'trigger'")
	} else if val != nil {
		t.Errorf("Inputs[\"trigger\"] = %v, want nil", val)
	}
	// sleep_time should resolve normally
	if got := task.Inputs["sleep_time"]; got != 5 {
		t.Errorf("Inputs[\"sleep_time\"] = %v, want 5", got)
	}
}

func TestResolveTaskInputs_MissingUpstreamOutput_NilOutputs(t *testing.T) {
	// Same as above but with nil Outputs map (not just empty).
	upstream := &model.Task{
		ID:     "task_0",
		StepID: "get_date",
		State:  model.TaskStateSuccess,
		// Outputs is nil
	}
	tasksByStepID := map[string]*model.Task{"get_date": upstream}

	task := &model.Task{ID: "task_1", StepID: "wait"}
	step := &model.Step{
		ID: "wait",
		In: []model.StepInput{
			{ID: "trigger", Source: "get_date/date_result"},
		},
	}

	if err := ResolveTaskInputs(task, step, nil, tasksByStepID, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val, exists := task.Inputs["trigger"]; !exists {
		t.Error("Inputs should contain key 'trigger'")
	} else if val != nil {
		t.Errorf("Inputs[\"trigger\"] = %v, want nil", val)
	}
}

func TestResolveTaskInputs_NormalizeDirectory_BareStringBVBRC(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "annotate"}
	step := &model.Step{
		ID: "annotate",
		In: []model.StepInput{
			{ID: "output_path", Source: "out_dir"},
		},
		Hints: &model.StepHints{
			BVBRCAppID:   "GenomeAnnotation",
			ExecutorType: model.ExecutorTypeBVBRC,
		},
		ToolInline: &model.Tool{
			ID:    "annotate_tool",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "output_path", Type: "Directory?"},
			},
		},
	}
	subInputs := map[string]any{"out_dir": "/awilke@bvbrc/home/gowe-test"}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, ok := task.Inputs["output_path"].(map[string]any)
	if !ok {
		t.Fatalf("output_path is %T, want map[string]any", task.Inputs["output_path"])
	}
	if dir["class"] != "Directory" {
		t.Errorf("class = %v, want Directory", dir["class"])
	}
	wantLoc := "ws:///awilke@bvbrc/home/gowe-test"
	if dir["location"] != wantLoc {
		t.Errorf("location = %v, want %v", dir["location"], wantLoc)
	}
}

func TestResolveTaskInputs_NormalizeDirectory_BareStringDocker(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "process"}
	step := &model.Step{
		ID: "process",
		In: []model.StepInput{
			{ID: "output_path", Source: "out_dir"},
		},
		Hints: &model.StepHints{
			ExecutorType: model.ExecutorTypeContainer,
			DockerImage:  "alpine:latest",
		},
		ToolInline: &model.Tool{
			ID:    "process_tool",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "output_path", Type: "Directory"},
			},
		},
	}
	subInputs := map[string]any{"out_dir": "/tmp/output"}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, ok := task.Inputs["output_path"].(map[string]any)
	if !ok {
		t.Fatalf("output_path is %T, want map[string]any", task.Inputs["output_path"])
	}
	if dir["class"] != "Directory" {
		t.Errorf("class = %v, want Directory", dir["class"])
	}
	wantLoc := "file:///tmp/output"
	if dir["location"] != wantLoc {
		t.Errorf("location = %v, want %v", dir["location"], wantLoc)
	}
}

func TestResolveTaskInputs_NormalizeDirectory_ExplicitScheme(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "output_path", Source: "out_dir"},
		},
		Hints: &model.StepHints{
			ExecutorType: model.ExecutorTypeBVBRC,
		},
		ToolInline: &model.Tool{
			ID:    "tool1",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "output_path", Type: "Directory"},
			},
		},
	}
	subInputs := map[string]any{"out_dir": "ws:///awilke@bvbrc/home/gowe-test"}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, ok := task.Inputs["output_path"].(map[string]any)
	if !ok {
		t.Fatalf("output_path is %T, want map[string]any", task.Inputs["output_path"])
	}
	if dir["location"] != "ws:///awilke@bvbrc/home/gowe-test" {
		t.Errorf("location = %v, want ws:///awilke@bvbrc/home/gowe-test", dir["location"])
	}
}

func TestResolveTaskInputs_NormalizeDirectory_MapPassthrough(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	dirObj := map[string]any{"class": "Directory", "location": "ws:///user/home/out"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "output_path", Source: "out_dir"},
		},
		Hints: &model.StepHints{ExecutorType: model.ExecutorTypeBVBRC},
		ToolInline: &model.Tool{
			ID:    "tool1",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "output_path", Type: "Directory"},
			},
		},
	}
	subInputs := map[string]any{"out_dir": dirObj}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dir, ok := task.Inputs["output_path"].(map[string]any)
	if !ok {
		t.Fatalf("output_path is %T, want map[string]any", task.Inputs["output_path"])
	}
	if dir["location"] != "ws:///user/home/out" {
		t.Errorf("location = %v, want ws:///user/home/out", dir["location"])
	}
}

func TestResolveTaskInputs_NormalizeDirectory_NilPassthrough(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "output_path", Source: "out_dir"},
		},
		Hints: &model.StepHints{ExecutorType: model.ExecutorTypeBVBRC},
		ToolInline: &model.Tool{
			ID:    "tool1",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "output_path", Type: "Directory?"},
			},
		},
	}
	subInputs := map[string]any{"out_dir": nil}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.Inputs["output_path"] != nil {
		t.Errorf("output_path = %v, want nil", task.Inputs["output_path"])
	}
}

func TestResolveTaskInputs_NormalizeDirectory_NonDirectoryUnchanged(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "step1"}
	step := &model.Step{
		ID: "step1",
		In: []model.StepInput{
			{ID: "name", Source: "user_name"},
		},
		Hints: &model.StepHints{ExecutorType: model.ExecutorTypeBVBRC},
		ToolInline: &model.Tool{
			ID:    "tool1",
			Class: "CommandLineTool",
			Inputs: []model.ToolInput{
				{ID: "name", Type: "string"},
			},
		},
	}
	subInputs := map[string]any{"user_name": "alice"}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.Inputs["name"] != "alice" {
		t.Errorf("name = %v, want alice", task.Inputs["name"])
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

func TestResolveTaskInputs_ValueFrom(t *testing.T) {
	task := &model.Task{ID: "task_1", StepID: "greet"}
	step := &model.Step{
		ID: "greet",
		In: []model.StepInput{
			{
				ID:        "message",
				Source:    "name",
				ValueFrom: "$(inputs.prefix + ' ' + self)",
			},
		},
	}
	subInputs := map[string]any{
		"prefix": "Hello",
		"name":   "World",
	}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := task.Inputs["message"]; got != "Hello World" {
		t.Errorf("Inputs[\"message\"] = %v, want \"Hello World\"", got)
	}
}

func TestResolveTaskInputs_ValueFrom_NoSource(t *testing.T) {
	// Test valueFrom without source (generates value purely from expression)
	task := &model.Task{ID: "task_1", StepID: "compute"}
	step := &model.Step{
		ID: "compute",
		In: []model.StepInput{
			{
				ID:        "computed_value",
				Source:    "",
				ValueFrom: "$(inputs.x * 2)",
			},
		},
	}
	subInputs := map[string]any{
		"x": 21,
	}

	if err := ResolveTaskInputs(task, step, subInputs, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expression evaluates to 42 (may be int64 or float64 depending on JS engine)
	var gotValue int64
	switch v := task.Inputs["computed_value"].(type) {
	case int64:
		gotValue = v
	case float64:
		gotValue = int64(v)
	default:
		t.Fatalf("Inputs[\"computed_value\"] = %T (%v), want numeric type", task.Inputs["computed_value"], task.Inputs["computed_value"])
	}
	if gotValue != 42 {
		t.Errorf("Inputs[\"computed_value\"] = %v, want 42", gotValue)
	}
}
