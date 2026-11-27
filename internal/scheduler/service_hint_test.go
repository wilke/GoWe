package scheduler

import (
	"context"
	"errors"
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

// serviceSubwfCWL is a parent whose step subwf_step runs sub-workflow svc-wf;
// its step serve runs inline tool svc-tool, which carries svcHints (inserted
// at the tool's level). With the hint the parser refuses it at registration.
func serviceSubwfCWL(svcHints string) string {
	return `
cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: SubworkflowFeatureRequirement
    inputs:
      port: int
    outputs: []
    steps:
      subwf_step:
        run: "#svc-wf"
        in:
          port: port
        out: []
  - id: svc-wf
    class: Workflow
    inputs:
      port: int
    outputs: []
    steps:
      serve:
        run:
          class: CommandLineTool
          id: svc-tool
          baseCommand: echo
          inputs:
            port:
              type: int
              inputBinding:
                position: 1
          outputs: []
` + svcHints + `        in:
          port: port
        out: []
`
}

const serviceSubwfHints = `          hints:
            gowe:Service:
              port_input: port
`

// storeParentBypassingParser stores a parent workflow whose model comes from
// modelCWL but whose RawCWL is rawCWL (what the scheduler re-parses for
// sub-workflows), as a workflow registered before the parser's nested-service
// check would be; plus a PENDING submission and its WAITING step instances.
// Returns the submission ID.
func storeParentBypassingParser(t *testing.T, st store.Store, modelCWL, rawCWL string, inputs map[string]any) string {
	t.Helper()
	ctx := context.Background()
	p := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if g, err := p.ParseGraph([]byte(rawCWL)); err != nil {
		t.Fatalf("ParseGraph(raw): %v", err)
	} else if _, err := p.ToModel(g, "x"); err == nil {
		t.Fatal("the parser accepts rawCWL; the test needs a document it refuses")
	}
	graph, err := p.ParseGraph([]byte(modelCWL))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf, err := p.ToModel(graph, "svc-subwf-test")
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
	sub := &model.Submission{
		ID: "sub_" + uuid.New().String(), WorkflowID: wf.ID, WorkflowName: wf.Name,
		State: model.SubmissionStatePending, Inputs: inputs, Outputs: map[string]any{},
		Labels: map[string]string{}, CreatedAt: now,
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	for _, step := range wf.Steps {
		si := &model.StepInstance{ID: "si_" + uuid.New().String(), SubmissionID: sub.ID, StepID: step.ID,
			State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now}
		if err := st.CreateStepInstance(ctx, si); err != nil {
			t.Fatalf("CreateStepInstance: %v", err)
		}
	}
	return sub.ID
}

// createChildSubmission refuses a child whose workflow is a service one
// (permanent error, no child stored).
func TestCreateChildSubmission_RefusesService(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	graph, err := parser.New(sched.logger).ParseGraph([]byte(serviceSubwfCWL(serviceSubwfHints)))
	if err != nil {
		t.Fatal(err)
	}
	subGraph := graph.SubWorkflows["svc-wf"]
	if subGraph == nil {
		t.Fatal("sub-workflow svc-wf not found")
	}
	parentTask := &model.Task{ID: "task_" + uuid.New().String(), StepID: "subwf_step"}
	parentSub := &model.Submission{ID: "sub_parent", Labels: map[string]string{}}
	child, err := sched.createChildSubmission(ctx, parentTask, subGraph, map[string]any{"port": 1}, parentSub,
		&model.Workflow{ID: "wf_parent"})
	if !errors.Is(err, errServiceInSubWorkflow) || child != nil {
		t.Fatalf("createChildSubmission = (%v, %v), want the service refusal", child, err)
	}
	if children, err := st.GetChildSubmissions(ctx, parentTask.ID); err != nil || len(children) != 0 {
		t.Fatalf("children = %v, %v; want none", children, err)
	}
}

// A stored parent whose sub-workflow holds a service (registered before the
// parser check): the scheduler fails the proxy and the step with the
// service error, creates no child, and never retries the proxy.
func TestSubwf_ServiceChildRefused(t *testing.T) {
	sched, st := testSetup(t)
	ctx := context.Background()
	subID := storeParentBypassingParser(t, st, serviceSubwfCWL(""), serviceSubwfCWL(serviceSubwfHints),
		map[string]any{"port": 8000})

	var sub *model.Submission
	for tick := 1; tick <= 10; tick++ {
		if err := sched.Tick(ctx); err != nil {
			t.Fatalf("Tick %d: %v", tick, err)
		}
		var err error
		if sub, err = st.GetSubmission(ctx, subID); err != nil {
			t.Fatal(err)
		}
		if sub.State.IsTerminal() {
			break
		}
	}
	if sub.State != model.SubmissionStateFailed {
		t.Fatalf("submission state = %s, want FAILED", sub.State)
	}
	for i := 0; i < 3; i++ { // more ticks: nothing is retried
		if err := sched.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	const want = "a service (gowe:Service) cannot run inside a sub-workflow"
	proxies := subworkflowProxies(t, st, subID)
	if len(proxies) != 1 {
		t.Fatalf("proxies = %d, want 1", len(proxies))
	}
	proxy := proxies[0]
	if proxy.State != model.TaskStateFailed || !strings.Contains(proxy.Stderr, want) || proxy.MaxRetries != 0 || proxy.RetryCount != 0 {
		t.Errorf("proxy = %s %q (max_retries %d, retry_count %d), want FAILED with %q, never retried",
			proxy.State, proxy.Stderr, proxy.MaxRetries, proxy.RetryCount, want)
	}
	si := getStepInstancesByStep(t, st, subID)["subwf_step"]
	if si == nil || si.State != model.StepStateFailed || !strings.Contains(si.Error, want) {
		t.Errorf("step = %+v, want FAILED with %q", si, want)
	}
	if children, err := st.GetChildSubmissions(ctx, proxy.ID); err != nil || len(children) != 0 {
		t.Errorf("children = %v, %v; want none", children, err)
	}
}

// A task of a child submission never carries a service, from the step's
// hints or from the template; the configured MaxRetries stays.
func TestCreateTaskFromStep_ChildDropsService(t *testing.T) {
	spec := func() *model.ServiceSpec {
		s := &model.ServiceSpec{PortInput: "port"}
		s.ApplyDefaults()
		return s
	}
	for _, c := range []struct {
		name  string
		hints *model.StepHints
		tmp   *model.RuntimeHints
	}{
		{"from the step", &model.StepHints{WorkerGroup: "gpu", Service: spec()}, nil},
		{"from the template", &model.StepHints{WorkerGroup: "gpu"}, &model.RuntimeHints{Service: spec()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sched, _ := testSetup(t)
			si := &model.StepInstance{ID: "si_1", SubmissionID: "sub_child", StepID: "s"}
			step := &model.Step{ID: "s", Hints: c.hints}
			sub := &model.Submission{ID: "sub_child", ParentTaskID: "task_parent"}
			task := sched.createTaskFromStep(si, &model.Task{RuntimeHints: c.tmp}, step, sub, model.ExecutorTypeWorker, -1)
			if task.RuntimeHints != nil && task.RuntimeHints.Service != nil {
				t.Fatalf("child task service = %+v, want none", task.RuntimeHints.Service)
			}
			if task.MaxRetries != sched.config.MaxRetries {
				t.Errorf("MaxRetries = %d, want the configured %d", task.MaxRetries, sched.config.MaxRetries)
			}
			if c.tmp != nil && c.tmp.Service == nil {
				t.Error("the template's hints were mutated")
			}
		})
	}
}

// The step's gowe:Service spec reaches the task as a private copy and pins
// MaxRetries to 0; a task without it keeps the configured MaxRetries.
func TestCreateTaskFromStep_ServicePropagation(t *testing.T) {
	newSpec := func() *model.ServiceSpec {
		s := &model.ServiceSpec{PortInput: "port", WeightsChecksum: "sha256:abc"}
		s.ApplyDefaults()
		return s
	}
	tests := []struct {
		name        string
		hints       *model.StepHints
		tmpHints    *model.RuntimeHints
		wantService bool
	}{
		{"service", &model.StepHints{WorkerGroup: "gpu", DockerImage: "vllm.sif", Service: newSpec()}, &model.RuntimeHints{}, true},
		{"service, template without hints", &model.StepHints{WorkerGroup: "gpu", Service: newSpec()}, nil, true},
		{"ordinary step", &model.StepHints{WorkerGroup: "gpu"}, &model.RuntimeHints{}, false},
		{"no hints", nil, &model.RuntimeHints{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, _ := testSetup(t)
			if sched.config.MaxRetries == 0 {
				t.Fatal("test needs a non-zero configured MaxRetries")
			}
			si := &model.StepInstance{ID: "si_1", SubmissionID: "sub_1", StepID: "s"}
			tmp := &model.Task{RuntimeHints: tt.tmpHints}
			step := &model.Step{ID: "s", Hints: tt.hints}
			sub := &model.Submission{ID: "sub_1"}
			task := sched.createTaskFromStep(si, tmp, step, sub, model.ExecutorTypeWorker, -1)

			var got *model.ServiceSpec
			if task.RuntimeHints != nil {
				got = task.RuntimeHints.Service
			}
			if !tt.wantService {
				if got != nil {
					t.Fatalf("service = %+v, want none", got)
				}
				if task.MaxRetries != sched.config.MaxRetries {
					t.Errorf("MaxRetries = %d, want the configured %d", task.MaxRetries, sched.config.MaxRetries)
				}
				return
			}
			if got == nil || *got != *tt.hints.Service {
				t.Fatalf("service = %+v, want %+v", got, tt.hints.Service)
			}
			if got == tt.hints.Service {
				t.Error("the task shares the workflow's spec; want a private copy")
			}
			if task.MaxRetries != 0 {
				t.Errorf("MaxRetries = %d, want 0 for a service task", task.MaxRetries)
			}
			if tt.tmpHints != nil && tt.tmpHints.Service != nil {
				t.Errorf("template hints were mutated: %+v", tt.tmpHints)
			}
		})
	}
}

// cloneRuntimeHints copies the Service spec, so a clone can be changed
// without touching the original.
func TestCloneRuntimeHints_Service(t *testing.T) {
	orig := &model.RuntimeHints{Service: &model.ServiceSpec{PortInput: "port", StopGraceSeconds: 30}}
	cp := cloneRuntimeHints(orig)
	if cp.Service == nil || *cp.Service != *orig.Service {
		t.Fatalf("clone service = %+v, want %+v", cp.Service, orig.Service)
	}
	cp.Service.StopGraceSeconds = 5
	if orig.Service.StopGraceSeconds != 30 {
		t.Error("changing the clone's service changed the original")
	}

	// ScrubSecrets keeps it.
	cp.ScrubSecrets()
	if cp.Service == nil {
		t.Error("ScrubSecrets dropped the service spec")
	}
}
