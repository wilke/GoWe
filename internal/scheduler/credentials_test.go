package scheduler

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

const credTestToken = "bvbrc|un=alice|expiry=9999999999|sig=zzz"

func hasCredential(task *model.Task) bool {
	h := task.RuntimeHints
	return h != nil && h.StagerOverrides != nil && h.StagerOverrides.HTTPCredential != nil &&
		h.StagerOverrides.HTTPCredential.Token == credTestToken
}

func remoteJob(scheme string) map[string]any {
	return map[string]any{
		"in": map[string]any{"class": "File", "location": scheme + "///alice@bvbrc/home/x.fa"},
	}
}

// TestAddUserToken verifies the staging-only gate on embedding the
// submitter's token (rewritten for the blessing policy): BV-BRC executor
// tasks, a granted injection, a worker task that needs the token for staging,
// and (deprecated) --token-inject-groups all embed the credential, and none of
// them ever sets InjectBVBRCToken.
func TestAddUserToken(t *testing.T) {
	tests := []struct {
		name         string
		executorType model.ExecutorType
		granted      bool // RuntimeHints.InjectBVBRCToken already set by a grant
		group        string
		tokenInject  []string
		job          map[string]any
		outputDest   string
		wantToken    bool
	}{
		{name: "bvbrc executor always gets the token", executorType: model.ExecutorTypeBVBRC, wantToken: true},
		{name: "granted worker task gets the token", executorType: model.ExecutorTypeWorker, granted: true, wantToken: true},
		{name: "worker with nothing to stage and no grant gets none", executorType: model.ExecutorTypeWorker, wantToken: false},
		{name: "worker with ws input gets it for staging", executorType: model.ExecutorTypeWorker, job: remoteJob("ws:"), wantToken: true},
		{name: "worker with shock input gets it for staging", executorType: model.ExecutorTypeWorker, job: remoteJob("shock:"), wantToken: true},
		{name: "worker with an output destination gets it for staging", executorType: model.ExecutorTypeWorker, outputDest: "ws:///alice@bvbrc/home/r/", wantToken: true},
		{name: "worker in a --token-inject-groups group gets it (staging only)", executorType: model.ExecutorTypeWorker, group: "esmfold", tokenInject: []string{"esmfold"}, wantToken: true},
		{name: "worker in another group gets none", executorType: model.ExecutorTypeWorker, group: "default", tokenInject: []string{"esmfold"}, wantToken: false},
		{name: "local executor never gets it for staging", executorType: model.ExecutorTypeLocal, job: remoteJob("ws:"), outputDest: "ws:///x/", wantToken: false},
		{name: "local executor in a token-inject group gets none", executorType: model.ExecutorTypeLocal, group: "esmfold", tokenInject: []string{"esmfold"}, wantToken: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loop{config: Config{TokenInjectGroups: tt.tokenInject}}
			task := &model.Task{
				ExecutorType: tt.executorType,
				Job:          tt.job,
				RuntimeHints: &model.RuntimeHints{WorkerGroup: tt.group, InjectBVBRCToken: tt.granted, TokenBlessingID: "tb"},
			}
			sub := &model.Submission{UserToken: credTestToken, OutputDestination: tt.outputDest}
			l.addUserToken(task, sub)
			if got := hasCredential(task); got != tt.wantToken {
				t.Errorf("credential embedded = %v, want %v", got, tt.wantToken)
			}
			if task.RuntimeHints.InjectBVBRCToken != tt.granted {
				t.Errorf("addUserToken changed InjectBVBRCToken to %v", task.RuntimeHints.InjectBVBRCToken)
			}
		})
	}

	t.Run("no submitter token: nothing embedded", func(t *testing.T) {
		l := &Loop{}
		task := &model.Task{ExecutorType: model.ExecutorTypeBVBRC}
		l.addUserToken(task, &model.Submission{})
		if task.RuntimeHints != nil && task.RuntimeHints.StagerOverrides != nil {
			t.Errorf("expected no credential, got %+v", task.RuntimeHints.StagerOverrides)
		}
	})
}

// TestAttachCredentials_GroupNeverSetsInject: --token-inject-groups is
// staging-only; it can embed a credential but never injects into the tool.
func TestAttachCredentials_GroupNeverSetsInject(t *testing.T) {
	l := &Loop{config: Config{TokenInjectGroups: []string{"esmfold"}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	task := &model.Task{ExecutorType: model.ExecutorTypeWorker, Tool: map[string]any{"class": "CommandLineTool"},
		RuntimeHints: &model.RuntimeHints{WorkerGroup: "esmfold"}}
	if err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s"}, &model.Workflow{}, &model.Submission{UserToken: credTestToken}); err != nil {
		t.Fatal(err)
	}
	if !hasCredential(task) {
		t.Error("group opt-in must still deliver the staging credential")
	}
	if task.RuntimeHints.InjectBVBRCToken || task.RuntimeHints.TokenBlessingID != "" {
		t.Errorf("group opt-in must never inject: %+v", task.RuntimeHints)
	}
}

// TestAttachCredentials_NeedBasedStaging is the blocker-1 table: the staging
// credential is delivered by NEED (worker executor + remote inputs or an
// output destination), identically in passthrough (wsStager nil) and server
// staging (wsStager set) modes.
func TestAttachCredentials_NeedBasedStaging(t *testing.T) {
	type cs struct {
		name       string
		job        map[string]any
		outputDest string
	}
	cases := []cs{
		{name: "ws input", job: remoteJob("ws:")},
		{name: "shock input", job: remoteJob("shock:")},
		{name: "output destination", outputDest: "ws:///alice@bvbrc/home/out/"},
		{name: "nothing to stage"},
	}
	execs := []struct {
		typ  model.ExecutorType
		want func(c cs) bool
	}{
		{model.ExecutorTypeWorker, func(c cs) bool { return c.name != "nothing to stage" }},
		{model.ExecutorTypeLocal, func(cs) bool { return false }},
		{model.ExecutorTypeBVBRC, func(cs) bool { return true }},
	}
	for _, staging := range []string{"passthrough", "server"} {
		for _, ex := range execs {
			for _, c := range cases {
				t.Run(staging+"/"+string(ex.typ)+"/"+c.name, func(t *testing.T) {
					l := &Loop{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
					if staging == "server" {
						l.wsStager = unreachableStager()
					}
					task := &model.Task{ExecutorType: ex.typ, Job: c.job, Tool: map[string]any{"class": "CommandLineTool"}}
					sub := &model.Submission{UserToken: credTestToken, OutputDestination: c.outputDest}
					if err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s"}, &model.Workflow{}, sub); err != nil {
						t.Fatal(err)
					}
					if got := hasCredential(task); got != ex.want(c) {
						t.Errorf("credential = %v, want %v", got, ex.want(c))
					}
					if task.RuntimeHints.InjectBVBRCToken {
						t.Error("staging delivery must never set InjectBVBRCToken")
					}
				})
			}
		}
	}
}

// --- grant tests -------------------------------------------------------------

const tokToolCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      s1:
        run: "#tok-tool"
        in: []
        out: [out]
  - id: tok-tool
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      "gowe:Execution":
        executor: worker
        inject_bvbrc_token: true
    inputs: []
    outputs:
      out:
        type: string
`

// tokToolStepGroupCWL is tokToolCWL with the step setting worker_group (and a
// bogus step-level inject request that must be inert).
const tokToolStepGroupCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      s1:
        run: "#tok-tool"
        in: []
        out: [out]
        hints:
          "gowe:Execution":
            worker_group: bvbrc
  - id: tok-tool
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      "gowe:Execution":
        executor: worker
        inject_bvbrc_token: true
    inputs: []
    outputs:
      out:
        type: string
`

const plainToolCWL = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      s1:
        run: "#plain-tool"
        in: []
        out: [out]
  - id: plain-tool
    class: CommandLineTool
    baseCommand: [echo]
    hints:
      "gowe:Execution":
        executor: worker
    inputs: []
    outputs:
      out:
        type: string
`

type credEnv struct {
	loop  *Loop
	st    store.Store
	subID string
	wfID  string
}

// newCredEnv builds a scheduler whose registry has the real WorkerExecutor
// (Submit just leaves the task QUEUED for a worker) and registers rawCWL with
// a token-carrying submission.
func newCredEnv(t *testing.T, rawCWL string, cfg func(*Config), outputDest string) *credEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := executor.NewRegistry(logger)
	reg.Register(executor.NewWorkerExecutor(st, logger))
	c := DefaultConfig()
	c.WorkspaceStaging = ""
	c.PreflightDeferralTicks = 0 // no workers are online in these tests
	if cfg != nil {
		cfg(&c)
	}
	l := NewLoop(st, reg, c, logger)

	ctx := context.Background()
	p := parser.New(logger)
	graph, err := p.ParseGraph([]byte(rawCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "cred-test")
	if err != nil {
		t.Fatalf("ToModel: %v", err)
	}
	now := time.Now().UTC()
	wf.ID = "wf_" + uuid.New().String()
	wf.RawCWL = rawCWL
	wf.Class = "Workflow"
	wf.CreatedAt, wf.UpdatedAt = now, now
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	sub := &model.Submission{
		ID: "sub_" + uuid.New().String(), WorkflowID: wf.ID, WorkflowName: wf.Name,
		State: model.SubmissionStatePending, Inputs: map[string]any{}, Outputs: map[string]any{},
		Labels: map[string]string{}, UserToken: credTestToken, AuthProvider: "bvbrc",
		OutputDestination: outputDest, CreatedAt: now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatal(err)
	}
	for _, step := range wf.Steps {
		si := &model.StepInstance{ID: "si_" + uuid.New().String(), SubmissionID: sub.ID, StepID: step.ID,
			State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatal(err)
		}
	}
	return &credEnv{loop: l, st: st, subID: sub.ID, wfID: wf.ID}
}

// run ticks until the step has a task and returns it.
func (e *credEnv) run(t *testing.T) *model.Task {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := e.loop.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
		tasks, err := e.st.ListTasksBySubmission(ctx, e.subID)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) > 0 {
			return tasks[0]
		}
	}
	t.Fatal("no task was created")
	return nil
}

func (e *credEnv) bless(t *testing.T, rawCWL string, groups []string) *model.ToolBlessing {
	t.Helper()
	res, err := toolhash.DigestForStep(rawCWL, "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &model.ToolBlessing{
		ID: "tb_" + uuid.New().String(), ToolHash: res.Hash, ToolName: "tok-tool",
		ImageRef: "python.sif", ImageSHA256s: []string{strings.Repeat("ab", 32)},
		WorkerGroups: groups, BlessedBy: "admin", BlessedAt: time.Now().UTC(),
	}
	if err := e.st.CreateToolBlessing(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestApplyTokenGrant_Unblessed_FailsPreDispatch(t *testing.T) {
	e := newCredEnv(t, tokToolCWL, nil, "")
	task := e.run(t)
	if task.State != model.TaskStateFailed {
		t.Fatalf("unblessed token request: state = %s, want FAILED", task.State)
	}
	if !strings.Contains(task.Stderr, "not blessed") || !strings.Contains(task.Stderr, "sha256:") {
		t.Errorf("stderr should name the problem and hash: %q", task.Stderr)
	}
	if hasCredential(task) || (task.RuntimeHints != nil && task.RuntimeHints.InjectBVBRCToken) {
		t.Errorf("a denied task must carry no token: %+v", task.RuntimeHints)
	}
}

func TestApplyTokenGrant_Blessed_Granted(t *testing.T) {
	e := newCredEnv(t, tokToolCWL, nil, "")
	b := e.bless(t, tokToolCWL, nil)
	task := e.run(t)
	if task.State != model.TaskStateQueued {
		t.Fatalf("state = %s (%s), want QUEUED", task.State, task.Stderr)
	}
	h := task.RuntimeHints
	if !h.InjectBVBRCToken || h.TokenBlessingID != b.ID || h.ToolHash != b.ToolHash {
		t.Errorf("grant not applied: %+v", h)
	}
	if len(h.TokenEnvNames) != 2 || h.TokenEnvNames[0] != "BVBRC_TOKEN" {
		t.Errorf("env names = %v", h.TokenEnvNames)
	}
	if len(h.ImageSHA256s) != 1 || h.ImageSHA256s[0] != b.ImageSHA256s[0] {
		t.Errorf("image digests = %v", h.ImageSHA256s)
	}
	if !hasCredential(task) {
		t.Error("a granted task must carry the credential")
	}
}

// A blessed tool keeps its request when the step sets worker_group (F3), and
// the blessing's group list is checked against the step-chosen group.
func TestApplyTokenGrant_StepWorkerGroup(t *testing.T) {
	t.Run("allowed group", func(t *testing.T) {
		e := newCredEnv(t, tokToolStepGroupCWL, nil, "")
		e.bless(t, tokToolStepGroupCWL, []string{"bvbrc"})
		task := e.run(t)
		if task.State != model.TaskStateQueued || !task.RuntimeHints.InjectBVBRCToken {
			t.Fatalf("state=%s stderr=%q hints=%+v", task.State, task.Stderr, task.RuntimeHints)
		}
		if task.RuntimeHints.WorkerGroup != "bvbrc" {
			t.Errorf("group = %q", task.RuntimeHints.WorkerGroup)
		}
	})
	t.Run("denied group", func(t *testing.T) {
		e := newCredEnv(t, tokToolStepGroupCWL, nil, "")
		e.bless(t, tokToolStepGroupCWL, []string{"esmfold"})
		task := e.run(t)
		if task.State != model.TaskStateFailed || !strings.Contains(task.Stderr, `worker group "bvbrc"`) {
			t.Fatalf("state=%s stderr=%q", task.State, task.Stderr)
		}
		if hasCredential(task) {
			t.Error("denied task carries a credential")
		}
	})
}

func TestApplyTokenGrant_Revoked_Denied(t *testing.T) {
	e := newCredEnv(t, tokToolCWL, nil, "")
	b := e.bless(t, tokToolCWL, nil)
	if err := e.st.RevokeToolBlessing(context.Background(), b.ID, "admin", "oops"); err != nil {
		t.Fatal(err)
	}
	task := e.run(t)
	if task.State != model.TaskStateFailed || !strings.Contains(task.Stderr, "not blessed") {
		t.Fatalf("revoked blessing must deny: state=%s stderr=%q", task.State, task.Stderr)
	}
}

func TestApplyTokenGrant_NonWorkerExecutor_Denied(t *testing.T) {
	l := &Loop{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, typ := range []model.ExecutorType{model.ExecutorTypeLocal, model.ExecutorTypeContainer, model.ExecutorTypeApptainer} {
		task := &model.Task{ExecutorType: typ, StepID: "s1", Tool: map[string]any{
			"class": "CommandLineTool",
			"hints": map[string]any{"gowe:Execution": map[string]any{"inject_bvbrc_token": true}},
		}}
		err := l.attachCredentials(context.Background(), task, &model.Step{ID: "s1"}, &model.Workflow{}, &model.Submission{UserToken: credTestToken})
		if err == nil || !strings.Contains(err.Error(), "only honoured on the worker executor") {
			t.Errorf("%s: err = %v", typ, err)
		}
		if task.RuntimeHints.InjectBVBRCToken {
			t.Errorf("%s: inject set", typ)
		}
	}
}

// A non-requesting tool is untouched by the policy: no grant, hash stamped.
func TestApplyTokenGrant_NoRequest_NoGrant(t *testing.T) {
	e := newCredEnv(t, plainToolCWL, nil, "")
	task := e.run(t)
	if task.State != model.TaskStateQueued {
		t.Fatalf("state=%s %s", task.State, task.Stderr)
	}
	if task.RuntimeHints.InjectBVBRCToken || task.RuntimeHints.TokenBlessingID != "" {
		t.Errorf("unexpected grant: %+v", task.RuntimeHints)
	}
	if !strings.HasPrefix(task.RuntimeHints.ToolHash, "sha256:") {
		t.Errorf("tool hash not stamped: %q", task.RuntimeHints.ToolHash)
	}
	if hasCredential(task) {
		t.Error("nothing to stage: no credential expected")
	}
}

// Output destination makes a non-requesting worker task carry the credential
// for staging only (both modes), and never inject.
func TestAttachCredentials_OutputDestination_StagingOnly_BothModes(t *testing.T) {
	for _, mode := range []string{"", "server"} {
		t.Run("mode="+mode, func(t *testing.T) {
			e := newCredEnv(t, plainToolCWL, func(c *Config) { c.WorkspaceStaging = mode }, "ws:///alice@bvbrc/home/out/")
			if mode == "server" {
				e.loop.SetWorkspaceStager(unreachableStager())
			}
			task := e.run(t)
			if !hasCredential(task) || task.RuntimeHints.InjectBVBRCToken {
				t.Fatalf("want staging credential without inject: %+v", task.RuntimeHints)
			}
		})
	}
}

func TestDevMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "python.sif"), []byte("fake image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("unblessed request is granted with a server-computed digest", func(t *testing.T) {
		e := newCredEnv(t, tokToolCWL, func(c *Config) { c.TokenBlessing = TokenBlessingDev; c.DevImageDir = dir }, "")
		task := e.run(t)
		h := task.RuntimeHints
		if task.State != model.TaskStateQueued || !h.InjectBVBRCToken || h.TokenBlessingID != DevBlessingID {
			t.Fatalf("state=%s stderr=%q hints=%+v", task.State, task.Stderr, h)
		}
		if len(h.ImageSHA256s) != 1 || len(h.ImageSHA256s[0]) != 64 {
			t.Errorf("dev grant must pin the image digest: %v", h.ImageSHA256s)
		}
		if len(h.TokenEnvNames) == 0 {
			t.Error("dev grant must name the env vars")
		}
	})
	t.Run("no dev image dir: fail closed", func(t *testing.T) {
		e := newCredEnv(t, tokToolCWL, func(c *Config) { c.TokenBlessing = TokenBlessingDev }, "")
		task := e.run(t)
		if task.State != model.TaskStateFailed || !strings.Contains(task.Stderr, "--dev-image-dir") {
			t.Fatalf("state=%s stderr=%q", task.State, task.Stderr)
		}
	})
	t.Run("missing image file: fail closed", func(t *testing.T) {
		e := newCredEnv(t, tokToolCWL, func(c *Config) { c.TokenBlessing = TokenBlessingDev; c.DevImageDir = t.TempDir() }, "")
		task := e.run(t)
		if task.State != model.TaskStateFailed {
			t.Fatalf("state=%s", task.State)
		}
	})
	t.Run("blessed tool behaves identically in dev mode", func(t *testing.T) {
		e := newCredEnv(t, tokToolCWL, func(c *Config) { c.TokenBlessing = TokenBlessingDev; c.DevImageDir = dir }, "")
		b := e.bless(t, tokToolCWL, nil)
		task := e.run(t)
		if task.RuntimeHints.TokenBlessingID != b.ID {
			t.Errorf("blessed tool must use its blessing in dev mode: %+v", task.RuntimeHints)
		}
	})
	t.Run("dev group restriction still applies to a blessing", func(t *testing.T) {
		e := newCredEnv(t, tokToolCWL, func(c *Config) { c.TokenBlessing = TokenBlessingDev; c.DevImageDir = dir }, "")
		e.bless(t, tokToolCWL, []string{"esmfold"})
		task := e.run(t)
		if task.State != model.TaskStateFailed {
			t.Fatalf("state=%s", task.State)
		}
	})
}

// TestRetry_ReattachesTokenAndRechecksBlessing: the retry re-runs the whole
// policy — the credential scrubbed at the FAILED state comes back, and a
// blessing revoked in the meantime fails the retry.
func TestRetry_ReattachesTokenAndRechecksBlessing(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*credEnv, *model.Task, *model.ToolBlessing) {
		e := newCredEnv(t, tokToolCWL, nil, "")
		b := e.bless(t, tokToolCWL, nil)
		task := e.run(t)
		if task.State != model.TaskStateQueued {
			t.Fatalf("state=%s %s", task.State, task.Stderr)
		}
		// Simulate the terminal scrub + RETRYING that follows a failed attempt.
		task.RuntimeHints.ScrubSecrets()
		task.State = model.TaskStateRetrying
		if err := e.st.UpdateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		task, err := e.st.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if hasCredential(task) {
			t.Fatal("scrub should have cleared the credential")
		}
		e.loop.cache = newTickCache()
		return e, task, b
	}

	t.Run("still blessed", func(t *testing.T) {
		e, task, b := setup(t)
		if failed := e.loop.reattachCredentialsForRetry(ctx, task); failed {
			t.Fatalf("retry failed: %s", task.Stderr)
		}
		if !hasCredential(task) || !task.RuntimeHints.InjectBVBRCToken || task.RuntimeHints.TokenBlessingID != b.ID {
			t.Errorf("retry did not re-attach: %+v", task.RuntimeHints)
		}
	})
	t.Run("revoked meanwhile", func(t *testing.T) {
		e, task, b := setup(t)
		if err := e.st.RevokeToolBlessing(ctx, b.ID, "admin", "bad"); err != nil {
			t.Fatal(err)
		}
		task.State = model.TaskStateScheduled // as claimed by resubmitRetrying
		if failed := e.loop.reattachCredentialsForRetry(ctx, task); !failed {
			t.Fatal("a revoked blessing must fail the retry")
		}
		got, err := e.st.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != model.TaskStateFailed || !strings.Contains(got.Stderr, "not blessed") {
			t.Errorf("state=%s stderr=%q", got.State, got.Stderr)
		}
		if got.RuntimeHints.InjectBVBRCToken || hasCredential(got) {
			t.Errorf("failed retry still carries the grant: %+v", got.RuntimeHints)
		}
	})
}

// The step cannot request the token: a step-level gowe:Execution
// inject_bvbrc_token on a tool that does not ask is merged into the final
// tool document, changes its hash, and is therefore refused as unblessed.
func TestStepLevelTokenRequest_IsNotHonoured(t *testing.T) {
	const raw = `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      s1:
        run: "#plain-tool"
        in: []
        out: [out]
        hints:
          "gowe:Execution":
            inject_bvbrc_token: true
  - id: plain-tool
    class: CommandLineTool
    baseCommand: [echo]
    hints:
      "gowe:Execution":
        executor: worker
    inputs: []
    outputs:
      out:
        type: string
`
	e := newCredEnv(t, raw, nil, "")
	task := e.run(t)
	if task.RuntimeHints != nil && task.RuntimeHints.InjectBVBRCToken {
		t.Fatalf("a step-level request must never inject: %+v", task.RuntimeHints)
	}
	if hasCredential(task) {
		t.Error("step-level request must not embed the credential")
	}
}

const nestedTokCWL = `
cwlVersion: v1.2
$graph:
  - id: tok
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      "gowe:Execution":
        executor: worker
        inject_bvbrc_token: true
    inputs: []
    outputs:
      out:
        type: string
  - id: subwf
    class: Workflow
    inputs: []
    outputs: []
    steps:
      inner:
        run: "#tok"
        in: []
        out: [out]
        hints:
          "gowe:Execution":
            executor: worker
  - id: main
    class: Workflow
    requirements:
      SubworkflowFeatureRequirement: {}
    hints:
      EnvVarRequirement:
        envDef:
          - envName: P
            envValue: parent
    inputs: []
    outputs: []
    steps:
      sub:
        run: "#subwf"
        in: []
        out: [out]
`

// The submit-time dry run (DigestForStep on the child root) must equal the
// digest the child's dispatch computes: the blessing made from the dry-run
// digest grants the nested task, and parent-level hints are not inherited.
func TestNestedStep_DryRunEqualsDispatch_ChildRoot(t *testing.T) {
	e := newCredEnv(t, nestedTokCWL, nil, "")
	res, err := toolhash.DigestForStep(nestedTokCWL, "sub/inner", nil)
	if err != nil {
		t.Fatal(err)
	}
	b := &model.ToolBlessing{ID: "tb_nested", ToolHash: res.Hash, ToolName: "tok", ImageRef: "python.sif",
		ImageSHA256s: []string{strings.Repeat("ab", 32)}, BlessedBy: "a", BlessedAt: time.Now().UTC()}
	if err := e.st.CreateToolBlessing(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var found *model.Task
	for i := 0; i < 8 && found == nil; i++ {
		if err := e.loop.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		for _, st := range []model.TaskState{model.TaskStateQueued, model.TaskStateFailed} {
			ts, _ := e.st.GetTasksByState(ctx, st)
			for _, tk := range ts {
				if tk.StepID == "inner" {
					found = tk
				}
			}
		}
	}
	if found == nil {
		t.Fatal("nested task never dispatched")
	}
	if found.State != model.TaskStateQueued || found.RuntimeHints.TokenBlessingID != "tb_nested" {
		t.Fatalf("dispatch disagrees with dry run: state=%s stderr=%q hints=%+v", found.State, found.Stderr, found.RuntimeHints)
	}
}
