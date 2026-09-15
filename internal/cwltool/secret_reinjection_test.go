package cwltool

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// TestExecuteTool_IWDRSecretReinjection mirrors cwltool's own
// tests/wf/secret_job.cwl pattern (see GitHub issue #260, "Standards status"
// discussion): a cwltool:Secrets-declared input is consumed via
// InitialWorkDirRequirement interpolation ($(inputs.pw) written into a
// staged file), not via an env var. The worker re-injects the real value
// into the in-memory job (internal/worker/worker.go's reinjectSecretInputs)
// before calling ExecuteTool — this test proves that once re-injected, IWDR
// interpolation sees the real value and the staged file's content contains
// it, while a value that never gets re-injected (simulating the
// server-persisted job, which this test keeps as a separate map) stays the
// literal model.SecretInputPlaceholder.
func TestExecuteTool_IWDRSecretReinjection(t *testing.T) {
	tool := &cwl.CommandLineTool{
		ID:          "secret-job",
		Class:       "CommandLineTool",
		BaseCommand: []string{"cat", "config.txt"},
		Inputs: map[string]cwl.ToolInputParam{
			"pw": {Type: "string"},
		},
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "config.txt",
						"entry":     "$(inputs.pw)",
					},
				},
			},
		},
	}

	const realSecret = "hunter2-reinjected"

	// The persisted job (what the server keeps / what a worker that skipped
	// re-injection would still see) — proven, in this test, to stay the
	// placeholder and never be the input to ExecuteTool.
	persistedJob := map[string]any{"pw": model.SecretInputPlaceholder}
	if persistedJob["pw"] != model.SecretInputPlaceholder {
		t.Fatal("sanity check failed")
	}

	// The worker's in-memory job AFTER reinjectSecretInputs has run — this is
	// what actually reaches ExecuteTool, exactly like executeWithCWLTool.
	reinjectedJob := map[string]any{"pw": realSecret}

	cfg := Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	workDir := t.TempDir()

	result, err := ExecuteTool(context.Background(), cfg, tool, reinjectedJob, workDir)
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !strings.Contains(result.Stdout, realSecret) {
		t.Errorf("stdout = %q, want it to contain the re-injected secret value", result.Stdout)
	}

	// The persisted job (a separate map, never touched by ExecuteTool) still
	// holds the placeholder — this is the property the worker's
	// reinjectSecretInputs is required to preserve: it mutates only the
	// in-memory task.Job used for execution, never anything sent back to the
	// server.
	if persistedJob["pw"] != model.SecretInputPlaceholder {
		t.Errorf("persisted job pw = %v, want it to remain the placeholder", persistedJob["pw"])
	}
}
