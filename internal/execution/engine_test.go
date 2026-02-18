package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func TestEngine_ExecuteTool_Echo(t *testing.T) {
	// Create a simple echo tool.
	tool := &cwl.CommandLineTool{
		ID:          "echo-tool",
		BaseCommand: []string{"echo"},
		Inputs: map[string]cwl.ToolInputParam{
			"message": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
		},
		Outputs: map[string]cwl.ToolOutputParam{
			"out": {
				Type: "stdout",
			},
		},
	}

	inputs := map[string]any{
		"message": "hello world",
	}

	// Create temp workdir.
	workDir := filepath.Join(os.TempDir(), "gowe-test-echo")
	defer os.RemoveAll(workDir)

	engine := NewEngine(Config{})
	result, err := engine.ExecuteTool(context.Background(), tool, inputs, workDir)
	if err != nil {
		t.Fatalf("ExecuteTool failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	// Check that output was collected.
	outFile, ok := result.Outputs["out"].(map[string]any)
	if !ok {
		t.Fatalf("output 'out' not found or wrong type: %T", result.Outputs["out"])
	}

	if outFile["class"] != "File" {
		t.Errorf("output class = %v, want File", outFile["class"])
	}
}

func TestEngine_ExecuteTool_Touch(t *testing.T) {
	// Create a touch tool that creates a file.
	tool := &cwl.CommandLineTool{
		ID:          "touch-tool",
		BaseCommand: []string{"touch"},
		Inputs: map[string]cwl.ToolInputParam{
			"filename": {
				Type: "string",
				InputBinding: &cwl.InputBinding{
					Position: 1,
				},
			},
		},
		Outputs: map[string]cwl.ToolOutputParam{
			"result": {
				Type: "File",
				OutputBinding: &cwl.OutputBinding{
					Glob: "*.txt",
				},
			},
		},
	}

	inputs := map[string]any{
		"filename": "test-output.txt",
	}

	// Create temp workdir.
	workDir := filepath.Join(os.TempDir(), "gowe-test-touch")
	defer os.RemoveAll(workDir)

	engine := NewEngine(Config{})
	result, err := engine.ExecuteTool(context.Background(), tool, inputs, workDir)
	if err != nil {
		t.Fatalf("ExecuteTool failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	// Check that output was collected.
	outFile, ok := result.Outputs["result"].(map[string]any)
	if !ok {
		t.Fatalf("output 'result' not found or wrong type: %T", result.Outputs["result"])
	}

	if outFile["class"] != "File" {
		t.Errorf("output class = %v, want File", outFile["class"])
	}

	basename, _ := outFile["basename"].(string)
	if basename != "test-output.txt" {
		t.Errorf("basename = %q, want %q", basename, "test-output.txt")
	}
}

func TestFileStager_StageIn(t *testing.T) {
	// Create a temp file to stage.
	srcDir := filepath.Join(os.TempDir(), "gowe-stager-src")
	dstDir := filepath.Join(os.TempDir(), "gowe-stager-dst")
	defer os.RemoveAll(srcDir)
	defer os.RemoveAll(dstDir)

	os.MkdirAll(srcDir, 0o755)
	os.MkdirAll(dstDir, 0o755)

	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("test content"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := NewFileStager("local")
	dstFile := filepath.Join(dstDir, "test.txt")

	err := stager.StageIn(context.Background(), "file://"+srcFile, dstFile)
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify content was copied.
	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("Read staged file failed: %v", err)
	}
	if string(content) != "test content" {
		t.Errorf("content = %q, want %q", string(content), "test content")
	}
}

