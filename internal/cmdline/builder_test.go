package cmdline

import (
	"reflect"
	"testing"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

func TestBuilder_SimpleCommand(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "echo",
		Inputs: map[string]cwl.ToolInputParam{
			"message": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
		},
	}

	inputs := map[string]any{
		"message": "hello world",
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"echo", "hello world"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_BaseCommandArray(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: []any{"bwa", "mem"},
		Inputs: map[string]cwl.ToolInputParam{
			"threads": {
				Type: "int",
				InputBinding: &cwl.InputBinding{
					Position: 1,
					Prefix:   "-t",
				},
			},
		},
	}

	inputs := map[string]any{
		"threads": 4,
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"bwa", "mem", "-t", "4"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_PrefixSeparate(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "grep",
		Inputs: map[string]cwl.ToolInputParam{
			"pattern": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
					Prefix:   "-e",
					Separate: boolPtr(false),
				},
			},
		},
	}

	inputs := map[string]any{
		"pattern": "hello",
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"grep", "-ehello"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_PositionSorting(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "tool",
		Inputs: map[string]cwl.ToolInputParam{
			"last": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 10,
				},
			},
			"first": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
			"middle": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 5,
				},
			},
		},
	}

	inputs := map[string]any{
		"last":   "c",
		"first":  "a",
		"middle": "b",
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"tool", "a", "b", "c"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_Arguments(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "tool",
		Arguments: []any{
			"--verbose",
			cwl.Argument{
				Position:  1,
				Prefix:    "--output",
				ValueFrom: "result.txt",
			},
		},
		Inputs: map[string]cwl.ToolInputParam{
			"input": {
				Type: "File",
				InputBinding: &cwl.InputBinding{
					Position: 2,
				},
			},
		},
	}

	inputs := map[string]any{
		"input": map[string]any{
			"class": "File",
			"path":  "/data/input.txt",
		},
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"tool", "--verbose", "--output", "result.txt", "/data/input.txt"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_ValueFrom(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "echo",
		Inputs: map[string]cwl.ToolInputParam{
			"name": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position:  1,
					ValueFrom: "Hello, $(inputs.name)!",
				},
			},
		},
	}

	inputs := map[string]any{
		"name": "World",
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"echo", "Hello, World!"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_ArrayInput(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "cat",
		Inputs: map[string]cwl.ToolInputParam{
			"files": {
				Type: "File[]",
				InputBinding: &cwl.InputBinding{
					Position:      1,
					ItemSeparator: ",",
				},
			},
		},
	}

	inputs := map[string]any{
		"files": []any{
			map[string]any{"class": "File", "path": "/a.txt"},
			map[string]any{"class": "File", "path": "/b.txt"},
			map[string]any{"class": "File", "path": "/c.txt"},
		},
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"cat", "/a.txt,/b.txt,/c.txt"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_StdoutStderr(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "sort",
		Stdout:      "sorted_$(inputs.name).txt",
		Stderr:      "errors.log",
		Inputs: map[string]cwl.ToolInputParam{
			"name": {
				Type: "string",
			},
			"input": {
				Type: "File",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
		},
	}

	inputs := map[string]any{
		"name":  "data",
		"input": map[string]any{"class": "File", "path": "/input.txt"},
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if result.Stdout != "sorted_data.txt" {
		t.Errorf("Stdout = %q, want %q", result.Stdout, "sorted_data.txt")
	}
	if result.Stderr != "errors.log" {
		t.Errorf("Stderr = %q, want %q", result.Stderr, "errors.log")
	}
}

func TestBuilder_NullInput(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "tool",
		Inputs: map[string]cwl.ToolInputParam{
			"required": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
			"optional": {
				Type: "string?",
				InputBinding: &cwl.InputBinding{
					Position: 2,
					Prefix:   "--opt",
				},
			},
		},
	}

	inputs := map[string]any{
		"required": "value",
		"optional": nil,
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Optional null input should be omitted.
	want := []string{"tool", "value"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_RuntimeContext(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "tool",
		Arguments: []any{
			cwl.Argument{
				Position:  1,
				Prefix:    "--threads",
				ValueFrom: "$(runtime.cores)",
			},
		},
	}

	runtime := &cwlexpr.RuntimeContext{
		OutDir: "/output",
		TmpDir: "/tmp",
		Cores:  8,
		Ram:    4096,
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, nil, runtime)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"tool", "--threads", "8"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func TestBuilder_NoInputBinding(t *testing.T) {
	tool := &cwl.CommandLineTool{
		BaseCommand: "tool",
		Inputs: map[string]cwl.ToolInputParam{
			"data": {
				Type: "string",
				// No InputBinding - should not appear in command.
			},
			"visible": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
		},
	}

	inputs := map[string]any{
		"data":    "hidden",
		"visible": "shown",
	}

	builder := NewBuilder(nil)
	result, err := builder.Build(tool, inputs, nil)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []string{"tool", "shown"}
	if !reflect.DeepEqual(result.Command, want) {
		t.Errorf("Command = %v, want %v", result.Command, want)
	}
}

func boolPtr(b bool) *bool {
	return &b
}
