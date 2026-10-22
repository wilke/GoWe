package cwlrunner

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/validate"
)

// enumWorkflowCWL is a one-step workflow whose top-level input chunk_method
// (an enum, with a default of "fixed") is passed straight through to its
// tool's own chunk_method enum input, used to exercise #273's cwl-runner
// validation: the top-level job check (validateTopLevelInputs, run once
// after defaults are merged, before any step runs) and the null-takes-
// default fix in mergeWorkflowInputDefaults.
const enumWorkflowCWL = `
cwlVersion: v1.2
class: Workflow
inputs:
  chunk_method:
    type:
      type: enum
      symbols: [fixed, semantic]
    default: fixed
outputs:
  out:
    type: File
    outputSource: step/out
steps:
  step:
    run:
      class: CommandLineTool
      baseCommand: [echo, hello]
      inputs:
        chunk_method:
          type:
            type: enum
            symbols: [fixed, semantic]
      outputs:
        out:
          type: stdout
      stdout: output.txt
    in:
      chunk_method: chunk_method
    out: [out]
`

func writeEnumWorkflowFixture(t *testing.T, jobContent string) (cwlPath, jobPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	cwlPath = filepath.Join(tmpDir, "wf.cwl")
	jobPath = filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(cwlPath, []byte(enumWorkflowCWL), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}
	return cwlPath, jobPath
}

func newEnumTestRunner(t *testing.T) *Runner {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	runner := NewRunner(logger)
	runner.NoContainer = true
	runner.OutDir = filepath.Join(t.TempDir(), "output")
	return runner
}

// TestRunner_InputValidation_Enforce_BadJob: a job with an out-of-range
// enum value fails, by default (enforce), with a non-zero-exit-worthy error
// (Execute returns non-nil) that names the offending input, and does so
// before any step runs (the top-level job check happens up front).
func TestRunner_InputValidation_Enforce_BadJob(t *testing.T) {
	cwlPath, jobPath := writeEnumWorkflowFixture(t, "chunk_method: not-a-symbol\n")
	runner := newEnumTestRunner(t)

	var buf bytes.Buffer
	err := runner.Execute(context.Background(), cwlPath, jobPath, &buf)
	if err == nil {
		t.Fatal("Execute() error = nil, want a non-nil error for an out-of-range enum value")
	}
	if !strings.Contains(err.Error(), "chunk_method") {
		t.Errorf("error = %q, want it to name chunk_method", err.Error())
	}
}

// TestRunner_InputValidation_ExplicitNullTakesDefault: a job that writes
// `chunk_method: null` must resolve to the workflow input's declared
// default ("fixed") rather than being treated as "not supplied but present"
// (the mergeWorkflowInputDefaults bug this fixes) — so execution succeeds
// with no validation error.
func TestRunner_InputValidation_ExplicitNullTakesDefault(t *testing.T) {
	cwlPath, jobPath := writeEnumWorkflowFixture(t, "chunk_method: null\n")
	runner := newEnumTestRunner(t)

	var buf bytes.Buffer
	if err := runner.Execute(context.Background(), cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute() error = %v, want nil (explicit null should take the declared default)", err)
	}
}

// TestRunner_InputValidation_NoInputValidation_Skips: with InputValidation
// set to off (the --no-input-validation escape hatch), the same bad job
// that TestRunner_InputValidation_Enforce_BadJob rejects must run to
// completion instead.
func TestRunner_InputValidation_NoInputValidation_Skips(t *testing.T) {
	cwlPath, jobPath := writeEnumWorkflowFixture(t, "chunk_method: not-a-symbol\n")
	runner := newEnumTestRunner(t)
	runner.InputValidation = validate.ModeOff

	var buf bytes.Buffer
	if err := runner.Execute(context.Background(), cwlPath, jobPath, &buf); err != nil {
		t.Fatalf("Execute() error = %v, want nil with InputValidation=off", err)
	}
}

// secretWorkflowCWL declares a top-level "api_key" input (int) marked secret
// via a cwltool:Secrets hint, consumed by a one-step tool that never
// actually runs in the enforce-mode test below (validateTopLevelInputs
// rejects the bad job before any step executes).
const secretWorkflowCWL = `
cwlVersion: v1.2
class: Workflow
hints:
  "cwltool:Secrets":
    secrets: [api_key]
inputs:
  api_key: int
outputs:
  out:
    type: File
    outputSource: step/out
steps:
  step:
    run:
      class: CommandLineTool
      baseCommand: [echo, hello]
      inputs:
        api_key: int
      outputs:
        out:
          type: stdout
      stdout: output.txt
    in:
      api_key: api_key
    out: [out]
`

const secretValue = "hunter2-super-secret-value"

func writeSecretWorkflowFixture(t *testing.T, jobContent string) (cwlPath, jobPath string) {
	t.Helper()
	tmpDir := t.TempDir()
	cwlPath = filepath.Join(tmpDir, "wf.cwl")
	jobPath = filepath.Join(tmpDir, "job.yml")
	if err := os.WriteFile(cwlPath, []byte(secretWorkflowCWL), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jobPath, []byte(jobContent), 0644); err != nil {
		t.Fatal(err)
	}
	return cwlPath, jobPath
}

// TestRunner_InputValidation_SecretNeverEchoed_Enforce is the #273
// review-#7 regression test: a workflow input named by a cwltool:Secrets
// hint gets ParamSpec.Secret (via parser.ExtractSecretInputs) in
// validateTopLevelInputs, so a bad value for it is rejected (wrong type -
// int declared, a string job value) without ever putting the real value in
// the returned error.
func TestRunner_InputValidation_SecretNeverEchoed_Enforce(t *testing.T) {
	cwlPath, jobPath := writeSecretWorkflowFixture(t, "api_key: \""+secretValue+"\"\n")
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner := NewRunner(logger)
	runner.NoContainer = true
	runner.OutDir = filepath.Join(t.TempDir(), "output")

	var buf bytes.Buffer
	err := runner.Execute(context.Background(), cwlPath, jobPath, &buf)
	if err == nil {
		t.Fatal("Execute() error = nil, want a non-nil error for a mistyped secret input")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error = %q, want it to name api_key", err.Error())
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Errorf("error leaked the secret value: %q", err.Error())
	}
	if strings.Contains(logBuf.String(), secretValue) {
		t.Errorf("log output leaked the secret value: %q", logBuf.String())
	}
}

// TestRunner_InputValidation_SecretNeverEchoed_Warn: same mistyped secret
// input, in warn mode - execution proceeds, but the logged warning must
// still never contain the real value.
func TestRunner_InputValidation_SecretNeverEchoed_Warn(t *testing.T) {
	cwlPath, jobPath := writeSecretWorkflowFixture(t, "api_key: \""+secretValue+"\"\n")
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	runner := NewRunner(logger)
	runner.NoContainer = true
	runner.OutDir = filepath.Join(t.TempDir(), "output")
	runner.InputValidation = validate.ModeWarn

	var buf bytes.Buffer
	_ = runner.Execute(context.Background(), cwlPath, jobPath, &buf)

	if !strings.Contains(logBuf.String(), "api_key") {
		t.Errorf("log output = %q, want it to name api_key", logBuf.String())
	}
	if strings.Contains(logBuf.String(), secretValue) {
		t.Errorf("log output leaked the secret value: %q", logBuf.String())
	}
}
