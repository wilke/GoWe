package parser

import (
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// graphDoc builds a packed document with a tool carrying toolHints (YAML,
// indented under "hints:") / toolReqs and a step carrying stepHints /
// stepReqs.
func graphDoc(toolHints, toolReqs, stepHints, stepReqs string) string {
	var b strings.Builder
	b.WriteString(`cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps:
      s1:
        run: "#t"
        in: []
        out: [out]
`)
	if stepHints != "" {
		b.WriteString("        hints:\n" + stepHints)
	}
	if stepReqs != "" {
		b.WriteString("        requirements:\n" + stepReqs)
	}
	b.WriteString(`  - id: t
    class: CommandLineTool
    baseCommand: [echo]
    inputs: []
    outputs:
      out:
        type: string
`)
	if toolHints != "" {
		b.WriteString("    hints:\n" + toolHints)
	}
	if toolReqs != "" {
		b.WriteString("    requirements:\n" + toolReqs)
	}
	return b.String()
}

func modelStepHints(t *testing.T, doc string) (*model.StepHints, error) {
	t.Helper()
	p := testParser()
	graph, err := p.ParseGraph([]byte(doc))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	mw, err := p.ToModel(graph, "w")
	if err != nil {
		return nil, err
	}
	return mw.Steps[0].Hints, nil
}

func TestParser_StepLevelInjectIgnored(t *testing.T) {
	doc := graphDoc("", "", `          "gowe:Execution":
            inject_bvbrc_token: true
            worker_group: g1
`, "")
	h, err := modelStepHints(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("step routing hints were dropped")
	}
	if h.InjectBVBRCToken {
		t.Fatal("a step-level inject_bvbrc_token must never reach the merged hints")
	}
	if h.WorkerGroup != "g1" {
		t.Errorf("step worker_group lost: %+v", h)
	}
}

func TestParser_ToolInjectSurvivesStepWorkerGroup(t *testing.T) {
	doc := graphDoc(`      "gowe:Execution":
        inject_bvbrc_token: true
        executor: worker
        docker_image: tool.sif
        worker_group: toolgroup
`, "", `          "gowe:Execution":
            worker_group: stepgroup
            docker_image: step.sif
            executor: local
            gpu: true
            secret_env: [HF_TOKEN]
`, "")
	h, err := modelStepHints(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || !h.InjectBVBRCToken {
		t.Fatalf("the tool's own request must survive a step that sets worker_group: %+v", h)
	}
	if h.WorkerGroup != "stepgroup" || h.ExecutorType != "local" || !h.RequiresGPU {
		t.Errorf("step routing fields must win: %+v", h)
	}
	if len(h.SecretEnv) != 1 || h.SecretEnv[0] != "HF_TOKEN" {
		t.Errorf("step secret opt-in lost: %+v", h)
	}
	if h.DockerImage != "tool.sif" {
		t.Errorf("tool docker_image must win over the step's: %q", h.DockerImage)
	}
}

func TestParser_StepRoutingKeepsToolFieldsItDoesNotSet(t *testing.T) {
	doc := graphDoc(`      "gowe:Execution":
        executor: worker
        worker_group: toolgroup
        gpu: true
`, "", `          "gowe:Execution":
            bvbrc_app_id: App1
`, "")
	h, err := modelStepHints(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if h.ExecutorType != "worker" || h.WorkerGroup != "toolgroup" || !h.RequiresGPU || h.BVBRCAppID != "App1" {
		t.Errorf("field-wise merge wrong: %+v", h)
	}
}

func TestParser_GoweExecutionUnderRequirementsRejected(t *testing.T) {
	cases := map[string]string{
		"tool requirements": graphDoc("", `      "gowe:Execution":
        inject_bvbrc_token: true
`, "", ""),
		"tool requirements legacy spelling": graphDoc("", `      goweHint:
        inject_bvbrc_token: true
`, "", ""),
		"step requirements": graphDoc("", "", "", `          "gowe:Execution":
            worker_group: g
`),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := modelStepHints(t, doc)
			if err == nil {
				t.Fatal("expected a registration error")
			}
			if !strings.Contains(err.Error(), "only honoured under hints") {
				t.Errorf("error should explain the hints-only rule: %v", err)
			}
		})
	}
	// Under hints it is fine.
	if _, err := modelStepHints(t, graphDoc(`      "gowe:Execution":
        inject_bvbrc_token: true
`, "", "", "")); err != nil {
		t.Errorf("hints spelling must be accepted: %v", err)
	}
}
