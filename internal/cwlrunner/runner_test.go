package cwlrunner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunner_Validate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)

	// Create a simple valid CWL tool
	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
    inputBinding:
      position: 1
outputs:
  output:
    type: stdout
stdout: message.txt
`
	tmpDir := t.TempDir()
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := runner.Validate(ctx, cwlPath); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestRunner_PrintDAG(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)

	// Create a simple CWL tool
	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
outputs: {}
`
	jobContent := `
message: "hello"
`
	tmpDir := t.TempDir()
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	jobPath := filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := context.Background()
	if err := runner.PrintDAG(ctx, cwlPath, jobPath, &buf); err != nil {
		t.Errorf("PrintDAG() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "workflow") {
		t.Errorf("PrintDAG() output missing workflow key: %s", output)
	}
	if !strings.Contains(output, "run_tool") {
		t.Errorf("PrintDAG() output missing run_tool step: %s", output)
	}
}

func TestRunner_PrintCommandLine(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)

	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
    inputBinding:
      position: 1
  flag:
    type: boolean?
    inputBinding:
      position: 0
      prefix: -v
outputs: {}
`
	jobContent := `
message: "hello world"
flag: true
`
	tmpDir := t.TempDir()
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	jobPath := filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := context.Background()
	if err := runner.PrintCommandLine(ctx, cwlPath, jobPath, &buf); err != nil {
		t.Errorf("PrintCommandLine() error = %v", err)
	}

	output := buf.String()
	// Check that the command contains the expected parts
	if !strings.Contains(output, "echo") {
		t.Errorf("PrintCommandLine() output missing 'echo': %s", output)
	}
	if !strings.Contains(output, "hello world") {
		t.Errorf("PrintCommandLine() output missing message: %s", output)
	}
}

func TestRunner_Execute_Local(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)
	runner.NoContainer = true

	tmpDir := t.TempDir()
	runner.OutDir = filepath.Join(tmpDir, "output")

	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  message:
    type: string
    inputBinding:
      position: 1
outputs:
  output:
    type: stdout
stdout: message.txt
`
	jobContent := `
message: "Hello, CWL!"
`
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	jobPath := filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	ctx := context.Background()
	if err := runner.Execute(ctx, cwlPath, jobPath, &buf); err != nil {
		t.Errorf("Execute() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "message.txt") {
		t.Errorf("Execute() output missing file reference: %s", output)
	}

	// Verify output file exists and has correct content
	outputFile := filepath.Join(runner.OutDir, "work", "message.txt")
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Errorf("Failed to read output file: %v", err)
	}
	if !strings.Contains(string(content), "Hello, CWL!") {
		t.Errorf("Output file has wrong content: %s", string(content))
	}
}

func TestRunner_LoadInputs_Empty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)

	inputs, err := runner.LoadInputs("")
	if err != nil {
		t.Errorf("LoadInputs() error = %v", err)
	}
	if len(inputs) != 0 {
		t.Errorf("LoadInputs() expected empty map, got %v", inputs)
	}
}

func TestResolveInputPaths(t *testing.T) {
	baseDir := "/base/dir"
	inputs := map[string]any{
		"file1": map[string]any{
			"class": "File",
			"path":  "relative/path.txt",
		},
		"file2": map[string]any{
			"class": "File",
			"path":  "/absolute/path.txt",
		},
		"string": "not a file",
		"array": []any{
			map[string]any{
				"class": "File",
				"path":  "in/array.txt",
			},
		},
	}

	resolved := resolveInputPaths(inputs, baseDir)

	// Check relative path was resolved
	file1 := resolved["file1"].(map[string]any)
	if file1["path"] != "/base/dir/relative/path.txt" {
		t.Errorf("Relative path not resolved: %v", file1["path"])
	}

	// Check absolute path was preserved
	file2 := resolved["file2"].(map[string]any)
	if file2["path"] != "/absolute/path.txt" {
		t.Errorf("Absolute path was modified: %v", file2["path"])
	}

	// Check string value was preserved
	if resolved["string"] != "not a file" {
		t.Errorf("String value was modified: %v", resolved["string"])
	}

	// Check array item was resolved
	array := resolved["array"].([]any)
	arrayFile := array[0].(map[string]any)
	if arrayFile["path"] != "/base/dir/in/array.txt" {
		t.Errorf("Array file path not resolved: %v", arrayFile["path"])
	}
}
