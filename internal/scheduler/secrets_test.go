package scheduler

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// --- addSecrets: gowe:Execution.secret_env / inject_secrets -----------------

func TestAddSecrets_SecretEnvAndInject(t *testing.T) {
	tests := []struct {
		name        string
		subSecrets  map[string]string
		hints       *model.StepHints
		wantSecrets map[string]string
		wantErrHas  string // substring the error must contain; "" means no error
	}{
		{
			name:        "inject_secrets: every submission secret attached",
			subSecrets:  map[string]string{"A": "va", "B": "vb"},
			hints:       &model.StepHints{InjectSecrets: true},
			wantSecrets: map[string]string{"A": "va", "B": "vb"},
		},
		{
			name:        "secret_env: only the named subset attached",
			subSecrets:  map[string]string{"A": "va", "B": "vb"},
			hints:       &model.StepHints{SecretEnv: []string{"A"}},
			wantSecrets: map[string]string{"A": "va"},
		},
		{
			name:       "secret_env: missing name errors, naming the secret",
			subSecrets: map[string]string{"A": "va"},
			hints:      &model.StepHints{SecretEnv: []string{"A", "C"}},
			wantErrHas: `"C"`,
		},
		{
			name:        "no hints on the step: nothing attached, no error",
			subSecrets:  map[string]string{"A": "va"},
			hints:       nil,
			wantSecrets: nil,
		},
		{
			name:        "hints present but submission has no secrets: nothing attached",
			subSecrets:  nil,
			hints:       &model.StepHints{InjectSecrets: true},
			wantSecrets: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loop{}
			task := &model.Task{ID: "task_1", StepID: "step1"}
			sub := &model.Submission{ID: "sub_1", Secrets: tt.subSecrets}
			wf := &model.Workflow{ID: "wf_1"}

			err := l.addSecrets(task, sub, tt.hints, wf)

			if tt.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("addSecrets() error = %v, want it to contain %q", err, tt.wantErrHas)
				}
				// No partial state may leak onto the task on failure.
				if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
					t.Errorf("task.RuntimeHints.Secrets = %v, want empty after a pre-dispatch failure", task.RuntimeHints.Secrets)
				}
				return
			}
			if err != nil {
				t.Fatalf("addSecrets() unexpected error: %v", err)
			}

			var got map[string]string
			if task.RuntimeHints != nil {
				got = task.RuntimeHints.Secrets
			}
			if len(got) != len(tt.wantSecrets) {
				t.Fatalf("task.RuntimeHints.Secrets = %v, want %v", got, tt.wantSecrets)
			}
			for k, v := range tt.wantSecrets {
				if got[k] != v {
					t.Errorf("secret %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestAddSecrets_DoesNotMutateSubmissionSecrets guards against sub.Secrets
// (which may be shared with a parent submission, see createChildSubmission)
// being aliased into task.RuntimeHints.Secrets.
func TestAddSecrets_DoesNotMutateSubmissionSecrets(t *testing.T) {
	l := &Loop{}
	subSecrets := map[string]string{"A": "va"}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: subSecrets}
	wf := &model.Workflow{ID: "wf_1"}

	if err := l.addSecrets(task, sub, &model.StepHints{InjectSecrets: true}, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	task.RuntimeHints.Secrets["A"] = "mutated"
	if subSecrets["A"] != "va" {
		t.Errorf("sub.Secrets was mutated via the attached task map: %v", subSecrets)
	}
}

// --- addSecrets: cwltool:Secrets re-injection --------------------------------

// secretStep builds a minimal Step with a single input sourced directly from
// a top-level workflow input (the only sourcing shape addSecrets handles).
func secretStep(stepID, stepInputID, workflowInputID string) *model.Step {
	return &model.Step{
		ID: stepID,
		In: []model.StepInput{
			{ID: stepInputID, Sources: []string{workflowInputID}},
		},
	}
}

func TestAddSecrets_CwltoolSecretsDirectSourcing(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{
		model.SecretNameForInput("pw"): "hunter2",
	}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Steps:        []model.Step{*secretStep("step1", "password", "pw")},
	}

	if err := l.addSecrets(task, sub, nil, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	if task.RuntimeHints == nil {
		t.Fatal("expected RuntimeHints to be allocated")
	}
	wantSecretName := model.SecretNameForInput("pw")
	if got := task.RuntimeHints.Secrets[wantSecretName]; got != "hunter2" {
		t.Errorf("task.RuntimeHints.Secrets[%q] = %q, want hunter2", wantSecretName, got)
	}
	wantEntry := "password=" + wantSecretName
	if len(task.RuntimeHints.SecretInputs) != 1 || task.RuntimeHints.SecretInputs[0] != wantEntry {
		t.Errorf("task.RuntimeHints.SecretInputs = %v, want [%q]", task.RuntimeHints.SecretInputs, wantEntry)
	}
}

// TestAddSecrets_CwltoolSecrets_IgnoresStepOutputSourcing verifies the
// documented out-of-scope case: a step input sourced from an UPSTREAM STEP
// OUTPUT (not a top-level workflow input) is left alone even if its source
// string happens to equal a name in wf.SecretInputs — the "/" in the source
// is what marks it as a step-output reference.
func TestAddSecrets_CwltoolSecrets_IgnoresStepOutputSourcing(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step2"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{
		model.SecretNameForInput("pw"): "hunter2",
	}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Steps: []model.Step{
			{ID: "step2", In: []model.StepInput{{ID: "in1", Sources: []string{"step1/pw"}}}},
		},
	}

	if err := l.addSecrets(task, sub, nil, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
		t.Errorf("step-output sourcing must not trigger re-injection, got %v", task.RuntimeHints.Secrets)
	}
}

func TestAddSecrets_CwltoolSecrets_MissingValue_Errors(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{}} // no value for "pw"
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Steps:        []model.Step{*secretStep("step1", "password", "pw")},
	}

	err := l.addSecrets(task, sub, nil, wf)
	if err == nil {
		t.Fatal("expected an error for a cwltool:Secrets input with no value in submission secrets")
	}
	if !strings.Contains(err.Error(), "password") && !strings.Contains(err.Error(), "pw") {
		t.Errorf("error = %v, want it to name the input", err)
	}
	if task.RuntimeHints != nil {
		t.Errorf("task.RuntimeHints = %+v, want nil (no partial attachment)", task.RuntimeHints)
	}
}

// TestAddSecrets_CwltoolSecrets_OptionalMissingValue_SkippedNotError is the
// M8 regression test: an optional workflow input (nullable "?" type, or a
// default) left unsupplied by the submitter never gets a placeholder/secret
// written at submission time (handler_submissions.go skips it), so there is
// nothing to re-inject — addSecrets must skip it, not fail the task
// pre-dispatch as it did before the fix.
func TestAddSecrets_CwltoolSecrets_OptionalMissingValue_SkippedNotError(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{}} // no value for "pw"
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Inputs:       []model.WorkflowInput{{ID: "pw", Type: "string?", Required: false}},
		Steps:        []model.Step{*secretStep("step1", "password", "pw")},
	}

	if err := l.addSecrets(task, sub, nil, wf); err != nil {
		t.Fatalf("addSecrets: unexpected error for an optional unsupplied secret input: %v", err)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.SecretInputs) != 0 {
		t.Errorf("task.RuntimeHints.SecretInputs = %v, want empty — nothing to re-inject", task.RuntimeHints.SecretInputs)
	}
}

// TestAddSecrets_CwltoolSecrets_RequiredMissingValue_StillErrors pins the
// M8 fix's boundary: a REQUIRED workflow input with no value in submission
// secrets is still a hard pre-dispatch failure — it can only mean the
// submission never supplied the secret at all, not "legitimately absent".
func TestAddSecrets_CwltoolSecrets_RequiredMissingValue_StillErrors(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Inputs:       []model.WorkflowInput{{ID: "pw", Type: "string", Required: true}},
		Steps:        []model.Step{*secretStep("step1", "password", "pw")},
	}

	err := l.addSecrets(task, sub, nil, wf)
	if err == nil {
		t.Fatal("expected an error for a required cwltool:Secrets input with no value in submission secrets")
	}
	if task.RuntimeHints != nil {
		t.Errorf("task.RuntimeHints = %+v, want nil (no partial attachment)", task.RuntimeHints)
	}
}

// TestAddSecrets_CwltoolSecrets_ValueFrom_Skipped is the M9 regression test:
// a step input that sources a cwltool:Secrets-declared workflow input AND
// carries a valueFrom expression must NOT be re-injected — doing so would
// silently overwrite the valueFrom-derived job value with the raw secret.
// Such an input keeps carrying the literal model.SecretInputPlaceholder.
func TestAddSecrets_CwltoolSecrets_ValueFrom_Skipped(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{
		model.SecretNameForInput("pw"): "hunter2",
	}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Steps: []model.Step{
			{
				ID: "step1",
				In: []model.StepInput{
					{ID: "password", Sources: []string{"pw"}, ValueFrom: "$(self.toUpperCase())"},
				},
			},
		},
	}

	if err := l.addSecrets(task, sub, nil, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.SecretInputs) != 0 {
		t.Errorf("task.RuntimeHints.SecretInputs = %v, want empty — valueFrom inputs are unsupported for re-injection", task.RuntimeHints.SecretInputs)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
		t.Errorf("task.RuntimeHints.Secrets = %v, want empty — nothing should be attached for a valueFrom-skipped input", task.RuntimeHints.Secrets)
	}
}

// TestAddSecrets_Atomic_NoPartialOnLaterFailure verifies that when a workflow
// declares multiple cwltool:Secrets inputs and only a later one is missing
// its value, the earlier (valid) one is NOT left attached to the task —
// addSecrets either fully succeeds or leaves the task exactly as it found it.
func TestAddSecrets_Atomic_NoPartialOnLaterFailure(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{
		model.SecretNameForInput("pw1"): "v1",
		// pw2 deliberately absent.
	}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw1", "pw2"},
		Steps: []model.Step{
			{
				ID: "step1",
				In: []model.StepInput{
					{ID: "a", Sources: []string{"pw1"}},
					{ID: "b", Sources: []string{"pw2"}},
				},
			},
		},
	}

	err := l.addSecrets(task, sub, nil, wf)
	if err == nil {
		t.Fatal("expected an error (pw2 missing)")
	}
	if task.RuntimeHints != nil {
		t.Errorf("task.RuntimeHints = %+v, want nil — pw1 must not leak through despite succeeding before pw2 failed", task.RuntimeHints)
	}
}

// TestAddSecrets_PopulatesSecretEnvNames is a direct M13 unit test: the
// names attached to RuntimeHints.SecretEnvNames must be exactly the
// secret_env/inject_secrets names — never the derived cwltool:Secrets
// INPUT_* name, which exists only for job re-injection.
func TestAddSecrets_PopulatesSecretEnvNames(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{
		"HF_TOKEN":                     "hf_abc",
		model.SecretNameForInput("pw"): "hunter2",
	}}
	wf := &model.Workflow{
		ID:           "wf_1",
		SecretInputs: []string{"pw"},
		Steps:        []model.Step{*secretStep("step1", "password", "pw")},
	}
	hints := &model.StepHints{SecretEnv: []string{"HF_TOKEN"}}

	if err := l.addSecrets(task, sub, hints, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	if task.RuntimeHints == nil {
		t.Fatal("expected RuntimeHints to be allocated")
	}
	if len(task.RuntimeHints.SecretEnvNames) != 1 || task.RuntimeHints.SecretEnvNames[0] != "HF_TOKEN" {
		t.Errorf("task.RuntimeHints.SecretEnvNames = %v, want [HF_TOKEN] (not the INPUT_PW re-injection name)", task.RuntimeHints.SecretEnvNames)
	}
	// Both values are still attached to Secrets (env delivery + job
	// re-injection both read from the same map) — only SecretEnvNames gates
	// what the executor is allowed to put in the container env.
	if len(task.RuntimeHints.Secrets) != 2 {
		t.Errorf("task.RuntimeHints.Secrets = %v, want both HF_TOKEN and %s", task.RuntimeHints.Secrets, model.SecretNameForInput("pw"))
	}
}

// TestAddSecrets_InjectSecrets_PopulatesSecretEnvNames verifies
// inject_secrets exposes every submission secret name (not just a
// SecretEnv-named subset).
func TestAddSecrets_InjectSecrets_PopulatesSecretEnvNames(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_1", StepID: "step1"}
	sub := &model.Submission{ID: "sub_1", Secrets: map[string]string{"A": "va", "B": "vb"}}
	wf := &model.Workflow{ID: "wf_1"}
	hints := &model.StepHints{InjectSecrets: true}

	if err := l.addSecrets(task, sub, hints, wf); err != nil {
		t.Fatalf("addSecrets: %v", err)
	}
	if task.RuntimeHints == nil {
		t.Fatal("expected RuntimeHints to be allocated")
	}
	got := map[string]bool{}
	for _, n := range task.RuntimeHints.SecretEnvNames {
		got[n] = true
	}
	if !got["A"] || !got["B"] || len(got) != 2 {
		t.Errorf("task.RuntimeHints.SecretEnvNames = %v, want exactly [A B]", task.RuntimeHints.SecretEnvNames)
	}
}

// --- scrubTaskToken: secrets clearing ----------------------------------------

func TestScrubTaskToken_ClearsSecretsKeepsSecretInputs(t *testing.T) {
	task := &model.Task{
		ID: "task_1",
		RuntimeHints: &model.RuntimeHints{
			Secrets:      map[string]string{"INPUT_PW": "hunter2"},
			SecretInputs: []string{"password=INPUT_PW"},
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: "user-token"},
			},
		},
	}

	scrubTaskToken(task)

	if task.RuntimeHints.Secrets != nil {
		t.Errorf("RuntimeHints.Secrets = %v, want nil after scrub", task.RuntimeHints.Secrets)
	}
	if task.RuntimeHints.StagerOverrides.HTTPCredential != nil {
		t.Errorf("HTTPCredential = %+v, want nil after scrub", task.RuntimeHints.StagerOverrides.HTTPCredential)
	}
	// SecretInputs names which inputs were secret, not their values: not sensitive, kept.
	if len(task.RuntimeHints.SecretInputs) != 1 || task.RuntimeHints.SecretInputs[0] != "password=INPUT_PW" {
		t.Errorf("SecretInputs = %v, want [password=INPUT_PW] (kept)", task.RuntimeHints.SecretInputs)
	}
}

func TestScrubTaskToken_NilRuntimeHints_NoPanic(t *testing.T) {
	task := &model.Task{ID: "task_1"}
	scrubTaskToken(task) // must not panic
}

// --- Sub-workflow proxy: no secrets; child inherits --------------------------

// TestCreateChildSubmission_InheritsSecrets_ProxyHasNone verifies that a child
// submission inherits Secrets/SecretNames/SecretsRetention from its parent,
// while the paired proxy task (which never executes) carries none at all —
// addSecrets is never called for sub-workflow dispatch, mirroring the
// existing "no addUserToken" rule for proxies.
func TestCreateChildSubmission_InheritsSecrets_ProxyHasNone(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p := parser.New(logger)
	graph, err := p.ParseGraph([]byte(nonScatterSubwfCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "child-secrets-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = nonScatterSubwfCWL
	wf.Class = "Workflow"
	wf.CreatedAt, wf.UpdatedAt = now, now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	sub := &model.Submission{
		ID:               "sub_" + uuid.New().String(),
		WorkflowID:       wf.ID,
		WorkflowName:     wf.Name,
		State:            model.SubmissionStatePending,
		Inputs:           map[string]any{"msg": "hello-child"},
		Outputs:          map[string]any{},
		Labels:           map[string]string{},
		Secrets:          map[string]string{"HF_TOKEN": "hf_abc123"},
		SecretNames:      []string{"HF_TOKEN"},
		SecretsRetention: "on_terminal",
		CreatedAt:        now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
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
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", step.ID, err)
		}
	}

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	proxies := subworkflowProxies(t, st, sub.ID)
	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy task, got %d", len(proxies))
	}
	proxy := proxies[0]
	if proxy.RuntimeHints != nil && len(proxy.RuntimeHints.Secrets) != 0 {
		t.Errorf("proxy.RuntimeHints.Secrets = %v, want empty — the proxy never executes", proxy.RuntimeHints.Secrets)
	}

	// GetChildSubmissions (used by onlyChild) does not select the secrets
	// columns, same as OutputDestination — re-read the full row.
	childRef := onlyChild(t, st, proxy.ID)
	child, err := st.GetSubmission(ctx, childRef.ID)
	if err != nil {
		t.Fatalf("GetSubmission(child): %v", err)
	}
	if len(child.Secrets) != 1 || child.Secrets["HF_TOKEN"] != "hf_abc123" {
		t.Errorf("child.Secrets = %v, want {HF_TOKEN: hf_abc123} (inherited)", child.Secrets)
	}
	if len(child.SecretNames) != 1 || child.SecretNames[0] != "HF_TOKEN" {
		t.Errorf("child.SecretNames = %v, want [HF_TOKEN]", child.SecretNames)
	}
	if child.SecretsRetention != "on_terminal" {
		t.Errorf("child.SecretsRetention = %q, want on_terminal", child.SecretsRetention)
	}
}

// --- Dispatch-site wiring: pre-dispatch failure end to end -------------------

// buildPipelineWithSecrets mirrors createPipeline but also sets the
// submission's secrets — createPipeline cannot, because store.UpdateSubmission
// deliberately never touches the secrets columns (they are write-once, set
// only by CreateSubmission/CreateSubmissionWithSteps).
func buildPipelineWithSecrets(t *testing.T, st store.Store, steps []model.Step, inputs map[string]any, secrets map[string]string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	wf := &model.Workflow{
		ID:         "wf_" + uuid.New().String(),
		Name:       "test-workflow",
		CWLVersion: "v1.2",
		Steps:      steps,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		Secrets:      secrets,
		SecretNames:  names,
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	for _, step := range steps {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: sub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", step.ID, err)
		}
	}
	return sub.ID
}

// TestDispatch_SecretEnvMissing_FailsTaskPreDispatch drives a real step
// through the full dispatch call site (not addSecrets directly): a
// secret_env name absent from the submission's secrets must fail the task
// pre-dispatch, with the task row itself created (State=FAILED, the name in
// Stderr) rather than left un-created, and the step/submission follow the
// task into FAILED — exactly as any other failed task would.
func TestDispatch_SecretEnvMissing_FailsTaskPreDispatch(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 3 // must NOT be retried — MaxRetries pinned to RetryCount
	ctx := context.Background()

	steps := []model.Step{
		{
			ID: "step1",
			ToolInline: &model.Tool{
				ID:          "t1",
				Class:       "CommandLineTool",
				BaseCommand: []string{"echo", "hi"},
			},
			Hints: &model.StepHints{SecretEnv: []string{"MISSING_SECRET"}},
		},
	}
	subID := buildPipelineWithSecrets(t, st, steps, map[string]any{}, map[string]string{"OTHER": "present"})

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task (created FAILED pre-dispatch), got %d", len(tasks))
	}
	task := tasks[0]
	if task.State != model.TaskStateFailed {
		t.Fatalf("task.State = %q, want FAILED", task.State)
	}
	if !strings.Contains(task.Stderr, "MISSING_SECRET") {
		t.Errorf("task.Stderr = %q, want it to name MISSING_SECRET", task.Stderr)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
		t.Errorf("task.RuntimeHints.Secrets = %v, want empty — no value anywhere in the task row", task.RuntimeHints.Secrets)
	}
	if task.MaxRetries != task.RetryCount {
		t.Errorf("task.MaxRetries = %d, RetryCount = %d, want equal (never retried)", task.MaxRetries, task.RetryCount)
	}
	if task.ExternalID != "" {
		t.Errorf("task.ExternalID = %q, want empty — the task was never dispatched to an executor", task.ExternalID)
	}

	si := getStepInstancesByStep(t, st, subID)["step1"]
	if si.State != model.StepStateFailed {
		t.Errorf("si.State = %q, want FAILED", si.State)
	}
	if !strings.Contains(si.Error, "MISSING_SECRET") {
		t.Errorf("si.Error = %q, want it to name MISSING_SECRET", si.Error)
	}

	sub, err := st.GetSubmission(ctx, subID)
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if sub.State != model.SubmissionStateFailed {
		t.Errorf("sub.State = %q, want FAILED", sub.State)
	}
	if sub.Error == nil || !strings.Contains(sub.Error.Context.Stderr, "MISSING_SECRET") {
		t.Errorf("sub.Error = %+v, want the secret name propagated into the submission error detail", sub.Error)
	}

	// Retrying (a subsequent tick) must not resurrect the task.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	tasks, _ = st.ListTasksBySubmission(ctx, subID)
	if len(tasks) != 1 {
		t.Errorf("expected the task count to stay at 1 (no retry), got %d", len(tasks))
	}
}

// scatterSecretEnvCWL: a scatter CommandLineTool step whose tool opts into
// gowe:Execution.secret_env for a name the test submission never provides.
// Scatter dispatch (dispatchScatterStep) resolves tmpTask.Job through
// populateToolAndJob, which needs wf.RawCWL — unlike the non-scatter dispatch
// test above, a bare (non-CWL) model.Step pipeline cannot exercise this path.
const scatterSecretEnvCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: ScatterFeatureRequirement
    inputs:
      letters: string[]
    outputs: []
    steps:
      scatter_step:
        run: "#echo-tool"
        scatter: letter
        in:
          letter: letters
        out: [out]
  - id: echo-tool
    class: CommandLineTool
    baseCommand: echo
    hints:
      "gowe:Execution":
        secret_env: [MISSING_SECRET]
    inputs:
      letter:
        type: string
        inputBinding:
          position: 1
    outputs:
      out:
        type: string
`

// registerGraphWorkflowWithSecrets is registerGraphWorkflow (see
// subworkflow_proxy_test.go) plus submission secrets — CreateSubmission is
// the only write path that can set them (see buildPipelineWithSecrets).
func registerGraphWorkflowWithSecrets(t *testing.T, st store.Store, rawCWL string, inputs map[string]any, secrets map[string]string) string {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	p := parser.New(logger)
	graph, err := p.ParseGraph([]byte(rawCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "secrets-scatter-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = rawCWL
	wf.Class = "Workflow"
	wf.CreatedAt, wf.UpdatedAt = now, now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wf.ID,
		WorkflowName: wf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		Secrets:      secrets,
		SecretNames:  names,
		CreatedAt:    now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
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
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance(%s): %v", step.ID, err)
		}
	}
	return sub.ID
}

// TestDispatch_ScatterSecretEnvMissing_FailsIterationPreDispatch mirrors
// TestDispatch_SecretEnvMissing_FailsTaskPreDispatch for the scatter dispatch
// call site (dispatchScatterStep), which attaches secrets independently per
// iteration.
func TestDispatch_ScatterSecretEnvMissing_FailsIterationPreDispatch(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()

	subID := registerGraphWorkflowWithSecrets(t, st, scatterSecretEnvCWL,
		map[string]any{"letters": []any{"a", "b"}},
		map[string]string{"OTHER": "present"})

	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	si := getStepInstancesByStep(t, st, subID)["scatter_step"]
	if si.State != model.StepStateFailed {
		t.Fatalf("si.State = %q, want FAILED", si.State)
	}
	if !strings.Contains(si.Error, "MISSING_SECRET") {
		t.Errorf("si.Error = %q, want it to name MISSING_SECRET", si.Error)
	}

	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil {
		t.Fatalf("ListTasksBySubmission: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 task (the first failing iteration), got %d", len(tasks))
	}
	if tasks[0].State != model.TaskStateFailed {
		t.Errorf("task.State = %q, want FAILED", tasks[0].State)
	}
	if !strings.Contains(tasks[0].Stderr, "MISSING_SECRET") {
		t.Errorf("task.Stderr = %q, want it to name MISSING_SECRET", tasks[0].Stderr)
	}
}

// TestAddSecrets_SecretEnvWithNoSubmissionSecrets_Fails pins the guard
// removed at integration: a tool that names a secret in secret_env must fail
// pre-dispatch when the submission supplied no secrets at all, not run
// silently without it.
func TestAddSecrets_SecretEnvWithNoSubmissionSecrets_Fails(t *testing.T) {
	l := &Loop{}
	task := &model.Task{ID: "task_x", StepID: "s1"}
	sub := &model.Submission{ID: "sub_x"} // no Secrets
	hints := &model.StepHints{SecretEnv: []string{"DB_DSN"}}
	wf := &model.Workflow{}
	err := l.addSecrets(task, sub, hints, wf)
	if err == nil {
		t.Fatal("expected error for secret_env with no submission secrets, got nil")
	}
	if !strings.Contains(err.Error(), "DB_DSN") {
		t.Fatalf("error should name the missing secret: %v", err)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
		t.Fatalf("no secrets must be attached on failure, got %v", task.RuntimeHints.Secrets)
	}
	// inject_secrets with no secrets stays a legitimate no-op.
	task2 := &model.Task{ID: "task_y", StepID: "s1"}
	if err := l.addSecrets(task2, sub, &model.StepHints{InjectSecrets: true}, wf); err != nil {
		t.Fatalf("inject_secrets with no submission secrets should be a no-op, got %v", err)
	}
}

// TestResubmitRetrying_ReattachesSecrets is the LOW regression test: a task
// that expected a delivered secret (secret_env) has that secret scrubbed the
// moment it first reaches a terminal FAILED state (scrubTaskToken, called
// from submitAndUpdateTask). Before the fix, resubmitRetrying resubmitted
// the same task object straight out of RETRYING with no call back into
// addSecrets, so the retried attempt ran with RuntimeHints.Secrets == nil —
// silently missing the secret it declared it needed, on the second attempt
// instead of the first.
//
// The tool's first invocation always fails (regardless of secrets), leaving
// a "seen" marker file in its task working directory (stable across
// retries: resubmitRetrying reuses the same task ID / working directory).
// The retried (second) invocation succeeds if and only if HF_TOKEN is set in
// its environment — which only happens if addSecrets ran again for the
// retry.
// retrySecretEnvCWL: a CommandLineTool whose first invocation always fails
// (leaving a "seen" marker file behind in its task working directory, which
// is stable across retries — resubmitRetrying reuses the same task ID) and
// whose retried (second) invocation succeeds if and only if HF_TOKEN is set
// in its environment. Registered via ParseGraph (not a bare model.Step
// pipeline) so the step dispatches through populateToolAndJob's real
// task.Tool/cwltool.ExecuteTool path — a bare-Step pipeline with no RawCWL
// falls back to the legacy _base_command executor path, which bypasses
// addSecrets/ApplySecrets entirely and would make this test pass for the
// wrong reason.
const retrySecretEnvCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      retry_step:
        run: "#retry-tool"
        in: []
        out: [out]
  - id: retry-tool
    class: CommandLineTool
    baseCommand: [sh, -c, 'test -f seen || { touch seen; exit 1; }; test -n "$HF_TOKEN"']
    hints:
      "gowe:Execution":
        secret_env: [HF_TOKEN]
    inputs: []
    outputs:
      out:
        type: string
`

func TestResubmitRetrying_ReattachesSecrets(t *testing.T) {
	sched, st := testSetup(t)
	sched.config.MaxRetries = 2
	ctx := context.Background()

	subID := registerGraphWorkflowWithSecrets(t, st, retrySecretEnvCWL,
		map[string]any{}, map[string]string{"HF_TOKEN": "hf_super_secret_value"})

	// Tick 1: dispatch (addSecrets attaches HF_TOKEN) -> tool fails (no
	// "seen" marker yet) -> scrubTaskToken clears RuntimeHints.Secrets ->
	// markRetries -> RETRYING.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	tasks, err := st.ListTasksBySubmission(ctx, subID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasksBySubmission after tick 1: err=%v, count=%d", err, len(tasks))
	}
	taskID := tasks[0].ID
	task, err := st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask after tick 1: %v", err)
	}
	if task.State != model.TaskStateRetrying {
		t.Fatalf("after tick 1: state = %q (stderr=%q), want RETRYING", task.State, task.Stderr)
	}
	if task.RuntimeHints != nil && len(task.RuntimeHints.Secrets) != 0 {
		t.Fatalf("after tick 1: RuntimeHints.Secrets = %v, want nil (scrubbed at the terminal FAILED state before RETRYING)", task.RuntimeHints.Secrets)
	}

	// Tick 2: resubmitRetrying must re-attach the secret before resubmitting
	// — the tool's second invocation only succeeds if HF_TOKEN reached its
	// environment.
	if err := sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	task, err = st.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask after tick 2: %v", err)
	}
	if task.State != model.TaskStateSuccess {
		t.Fatalf("after tick 2: state = %q (stderr=%q), want SUCCESS — the retry must have re-attached HF_TOKEN for the tool to see it",
			task.State, task.Stderr)
	}
}
