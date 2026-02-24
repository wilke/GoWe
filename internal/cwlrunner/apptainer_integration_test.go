//go:build integration

package cwlrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func skipIfNoApptainer(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("apptainer"); err != nil {
		t.Skip("Apptainer not available, skipping integration test")
	}
}

func TestCWLRunner_ApptainerEcho(t *testing.T) {
	skipIfNoApptainer(t)

	tmpDir := t.TempDir()

	// Write a simple CWL tool that uses DockerRequirement.
	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
requirements:
  DockerRequirement:
    dockerPull: alpine:latest
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
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Write a job file.
	jobContent := `message: hello from apptainer integration test`
	jobPath := filepath.Join(tmpDir, "echo-job.yml")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(tmpDir, "output")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runner := NewRunner(logger)
	runner.OutDir = outDir
	runner.ContainerRuntime = "apptainer"

	var buf bytes.Buffer
	ctx := context.Background()
	if err := runner.Execute(ctx, cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Parse the JSON output.
	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v\nraw output: %s", err, buf.String())
	}

	// Verify the output file was created.
	output, ok := result["output"].(map[string]any)
	if !ok {
		t.Fatalf("output is %T, want map; raw: %s", result["output"], buf.String())
	}

	if class := output["class"]; class != "File" {
		t.Errorf("output.class = %v, want File", class)
	}

	path, _ := output["path"].(string)
	if path == "" {
		t.Fatal("output.path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}

	if got := string(data); got != "hello from apptainer integration test\n" {
		t.Errorf("output contents = %q, want %q", got, "hello from apptainer integration test\n")
	}
}

func TestCWLRunner_ApptainerNoContainerOverride(t *testing.T) {
	skipIfNoApptainer(t)

	tmpDir := t.TempDir()

	// A tool with DockerRequirement — but NoContainer should force local execution.
	cwlContent := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
requirements:
  DockerRequirement:
    dockerPull: alpine:latest
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
	cwlPath := filepath.Join(tmpDir, "echo.cwl")
	if err := os.WriteFile(cwlPath, []byte(cwlContent), 0644); err != nil {
		t.Fatal(err)
	}

	jobContent := `message: local execution`
	jobPath := filepath.Join(tmpDir, "echo-job.yml")
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(tmpDir, "output")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runner := NewRunner(logger)
	runner.OutDir = outDir
	runner.ContainerRuntime = "apptainer"
	runner.NoContainer = true // Should override ContainerRuntime

	var buf bytes.Buffer
	ctx := context.Background()
	if err := runner.Execute(ctx, cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal output: %v\nraw: %s", err, buf.String())
	}

	// Verify it ran (local echo should work fine).
	output, ok := result["output"].(map[string]any)
	if !ok {
		t.Fatalf("output is %T, want map; raw: %s", result["output"], buf.String())
	}

	path, _ := output["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := string(data); got != "local execution\n" {
		t.Errorf("output = %q, want %q", got, "local execution\n")
	}
}
