package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// --- Retention: real Tick-driven completion, then a forced sweep ---
//
// Deliberately independent of task-level secret *delivery* (no
// gowe:Execution.secret_env / cwltool:Secrets involved, no worker): the
// retention sweep purges a submission's Secrets column based on its state
// and policy alone (internal/scheduler/secrets_retention.go), so a trivial
// single-step local echo pipeline — identical in shape to
// TestIntegration_TwoStepLocalPipeline — is enough to drive a submission to
// a real COMPLETED state via Tick() and then exercise the sweep for real,
// without depending on how (or whether) any step actually consumes a
// secret. The rate-limited sweep (secretsRetentionSweepInterval, once per
// minute) is forced by resetting the unexported Loop.lastSecretsSweep field
// — the test seam its own doc comment points at — available here because
// this file lives in package scheduler (white-box).

func retentionTestWorkflow(id string) *model.Workflow {
	return &model.Workflow{
		ID:         id,
		Name:       "retention-e2e",
		CWLVersion: "v1.2",
		RawCWL:     "test",
		Steps: []model.Step{
			{
				ID:      "step1",
				ToolRef: "echo-tool",
				ToolInline: &model.Tool{
					ID:          "echo-tool",
					Class:       "CommandLineTool",
					BaseCommand: []string{"echo", "hello"},
					Inputs:      []model.ToolInput{{ID: "dummy", Type: "string"}},
					Outputs:     []model.ToolOutput{},
				},
				DependsOn: []string{},
				In:        []model.StepInput{},
				Out:       []string{},
			},
		},
		Inputs:    []model.WorkflowInput{},
		Outputs:   []model.WorkflowOutput{},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

// retentionHarness bundles a real file-backed store + real Loop + real
// LocalExecutor for the trivial echo pipeline above.
type retentionHarness struct {
	t     *testing.T
	ctx   context.Context
	store store.Store
	loop  *Loop
}

func newRetentionHarness(t *testing.T) *retentionHarness {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := executor.NewRegistry(logger)
	reg.Register(executor.NewLocalExecutor(t.TempDir(), logger))
	cfg := DefaultConfig()
	cfg.MaxRetries = 0
	loop := NewLoop(st, reg, cfg, logger)
	return &retentionHarness{t: t, ctx: context.Background(), store: st, loop: loop}
}

// runWithSecrets registers retentionTestWorkflow, submits it carrying the
// given secrets/retention policy, and ticks to a terminal state.
func (h *retentionHarness) runWithSecrets(secrets map[string]string, retention string) *model.Submission {
	h.t.Helper()
	wf := retentionTestWorkflow("wf_" + uuid.New().String())
	if err := h.store.CreateWorkflow(h.ctx, wf); err != nil {
		h.t.Fatalf("create workflow: %v", err)
	}

	var names []string
	for n := range secrets {
		names = append(names, n)
	}
	now := time.Now().UTC()
	sub := &model.Submission{
		ID:               "sub_" + uuid.New().String(),
		WorkflowID:       wf.ID,
		WorkflowName:     wf.Name,
		State:            model.SubmissionStatePending,
		Inputs:           map[string]any{},
		Outputs:          map[string]any{},
		Labels:           map[string]string{},
		CreatedAt:        now,
		Secrets:          secrets,
		SecretNames:      names,
		SecretsRetention: retention,
	}
	if err := h.store.CreateSubmission(h.ctx, sub); err != nil {
		h.t.Fatalf("create submission: %v", err)
	}
	for _, step := range wf.Steps {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := h.store.CreateStepInstance(h.ctx, si); err != nil {
			h.t.Fatalf("create step instance: %v", err)
		}
	}

	const maxTicks = 10
	for i := 1; i <= maxTicks; i++ {
		if err := h.loop.Tick(h.ctx); err != nil {
			h.t.Fatalf("tick %d: %v", i, err)
		}
		got, err := h.store.GetSubmission(h.ctx, sub.ID)
		if err != nil {
			h.t.Fatalf("get submission: %v", err)
		}
		if got.State.IsTerminal() {
			return got
		}
	}
	h.t.Fatalf("submission did not reach a terminal state after %d ticks", maxTicks)
	return nil
}

func TestE2E_RetentionSweep_RealCompletion_OnTerminal(t *testing.T) {
	h := newRetentionHarness(t)
	got := h.runWithSecrets(map[string]string{"E2E_TOKEN": "s3cr3t-VALUE-9f2a-retention"}, "on_terminal")
	if got.State != model.SubmissionStateCompleted {
		t.Fatalf("submission state = %s, want COMPLETED", got.State)
	}
	// Note: Tick()'s very first call always runs the sweep unconditionally
	// (Loop.lastSecretsSweep starts zero, so the rate-limit check's
	// "!lastSecretsSweep.IsZero()" guard is false on tick 1) — so a trivial
	// single-step pipeline that reaches COMPLETED within its first tick can
	// already be purged by the time runWithSecrets returns. The forced
	// reset+extra tick below is kept anyway so this test also proves the
	// documented test seam (resetting the unexported field to force a sweep
	// past the once-per-minute rate limit) works on its own.
	h.loop.lastSecretsSweep = time.Time{}
	h.loop.lastSecretsSweep = time.Time{}
	if err := h.loop.Tick(h.ctx); err != nil {
		t.Fatalf("sweep tick: %v", err)
	}

	purged, err := h.store.GetSubmission(h.ctx, got.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if purged.SecretsState() != "purged" {
		t.Fatalf("SecretsState() = %s, want purged after on_terminal sweep", purged.SecretsState())
	}
	if len(purged.Secrets) != 0 {
		t.Errorf("Secrets = %v, want empty after purge", purged.Secrets)
	}
	if len(purged.SecretNames) == 0 {
		t.Errorf("SecretNames = %v, want retained for auditability after purge", purged.SecretNames)
	}
}

func TestE2E_RetentionSweep_RealCompletion_Keep(t *testing.T) {
	h := newRetentionHarness(t)
	got := h.runWithSecrets(map[string]string{"E2E_TOKEN": "s3cr3t-VALUE-9f2a-retention"}, "keep")
	if got.State != model.SubmissionStateCompleted {
		t.Fatalf("submission state = %s, want COMPLETED", got.State)
	}

	h.loop.lastSecretsSweep = time.Time{}
	if err := h.loop.Tick(h.ctx); err != nil {
		t.Fatalf("sweep tick: %v", err)
	}

	kept, err := h.store.GetSubmission(h.ctx, got.ID)
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if kept.SecretsState() != "present" {
		t.Errorf("SecretsState() = %s, want present (keep policy must survive the sweep)", kept.SecretsState())
	}
}
