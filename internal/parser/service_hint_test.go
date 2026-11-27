package parser

import (
	"errors"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// serviceGraphDoc is graphDoc with a tool srv that has an int input port and
// a string input name, sourced from workflow inputs.
func serviceGraphDoc(toolHints, toolReqs, stepHints, stepReqs string) string {
	var b strings.Builder
	b.WriteString(`cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      port: int
      name: string
    outputs: []
    steps:
      s1:
        run: "#srv"
        in:
          port: port
          name: name
        out: [out]
`)
	if stepHints != "" {
		b.WriteString("        hints:\n" + stepHints)
	}
	if stepReqs != "" {
		b.WriteString("        requirements:\n" + stepReqs)
	}
	b.WriteString(`  - id: srv
    class: CommandLineTool
    baseCommand: [serve]
    inputs:
      port: int
      name: string
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

// svcBody returns a tool-level gowe:Service block with the given body lines
// (indented for the tool's hints/requirements).
func svcBody(lines ...string) string {
	var b strings.Builder
	b.WriteString("      gowe:Service:\n")
	for _, l := range lines {
		b.WriteString("        " + l + "\n")
	}
	return b.String()
}

// defaultService is the spec of a hint that names only port_input.
func defaultService() model.ServiceSpec {
	return model.ServiceSpec{
		PortInput:        "port",
		Scheme:           "http",
		StopGraceSeconds: 30,
		Health: model.ServiceHealth{
			Path:                "/health",
			StartTimeoutSeconds: 600,
			IntervalSeconds:     10,
			FailureThreshold:    3,
		},
	}
}

func TestParser_ServiceHint(t *testing.T) {
	full := svcBody(
		"port_input: port",
		"scheme: https",
		"health:",
		"  path: /healthz",
		"  ready_path: /v1/models",
		"  ready_contains: my-model",
		"  start_timeout: 900",
		"  interval: 5",
		"  failure_threshold: 4",
		"stop_grace: 60",
		"weights_checksum: sha256:abc",
	)
	fullWant := model.ServiceSpec{
		PortInput:        "port",
		Scheme:           "https",
		StopGraceSeconds: 60,
		WeightsChecksum:  "sha256:abc",
		Health: model.ServiceHealth{
			Path:                "/healthz",
			ReadyPath:           "/v1/models",
			ReadyContains:       "my-model",
			StartTimeoutSeconds: 900,
			IntervalSeconds:     5,
			FailureThreshold:    4,
		},
	}
	stepGrace := defaultService()
	stepGrace.StopGraceSeconds = 5
	partialHealth := defaultService()
	partialHealth.Health.IntervalSeconds = 20

	tests := []struct {
		name      string
		toolHints string
		toolReqs  string
		stepHints string
		stepReqs  string
		want      model.ServiceSpec
	}{
		{name: "full hint", toolHints: full, want: fullWant},
		{name: "defaults", toolHints: svcBody("port_input: port"), want: defaultService()},
		{name: "partial health keeps other defaults", toolHints: svcBody("port_input: port", "health:", "  interval: 20"),
			want: partialHealth},
		{name: "in requirements", toolReqs: svcBody("port_input: port"), want: defaultService()},
		{name: "list-form hints", toolHints: "      - class: gowe:Service\n        port_input: port\n", want: defaultService()},
		{name: "beside other hints", toolHints: "      DockerRequirement:\n        dockerPull: vllm.sif\n" + svcBody("port_input: port"),
			want: defaultService()},
		{name: "step only", stepHints: "          gowe:Service:\n            port_input: port\n", want: defaultService()},
		{name: "step wins over tool", toolHints: svcBody("port_input: port"),
			stepHints: "          gowe:Service:\n            port_input: port\n            stop_grace: 5\n", want: stepGrace},
		{name: "step requirement", stepReqs: "          gowe:Service:\n            port_input: port\n", want: defaultService()},
		{name: "tool kept when step sets other hints", toolHints: svcBody("port_input: port"),
			stepHints: "          gowe:Execution:\n            worker_group: g\n", want: defaultService()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := modelStepHints(t, serviceGraphDoc(tt.toolHints, tt.toolReqs, tt.stepHints, tt.stepReqs))
			if err != nil {
				t.Fatal(err)
			}
			if h == nil || h.Service == nil {
				t.Fatalf("step hints = %+v, want a service", h)
			}
			if *h.Service != tt.want {
				t.Errorf("service = %+v, want %+v", *h.Service, tt.want)
			}
		})
	}
}

// A service-only hint keeps the hints (Service is part of the "any field
// set" condition), at tool and step level.
func TestParser_ServiceOnlyHintKept(t *testing.T) {
	p := testParser()
	graph, err := p.ParseGraph([]byte(serviceGraphDoc(svcBody("port_input: port"), "", "", "")))
	if err != nil {
		t.Fatal(err)
	}
	mw, err := p.ToModel(graph, "w")
	if err != nil {
		t.Fatal(err)
	}
	if th := mw.Steps[0].ToolInline.Hints; th == nil || th.Service == nil {
		t.Errorf("tool hints = %+v, want the service", th)
	}
	if sh := mw.Steps[0].Hints; sh == nil || sh.Service == nil {
		t.Errorf("step hints = %+v, want the service", sh)
	}

	h, err := extractStepHints(map[string]any{"gowe:Service": map[string]any{"port_input": "port"}}, nil)
	if err != nil || h == nil || h.Service == nil {
		t.Fatalf("extractStepHints = (%+v, %v), want hints with the service", h, err)
	}
}

// Without the hint nothing changes: hints stay nil, or carry no service.
func TestParser_NoServiceHint(t *testing.T) {
	h, err := modelStepHints(t, serviceGraphDoc("", "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Errorf("step hints = %+v, want nil", h)
	}
	h, err = modelStepHints(t, serviceGraphDoc("      gowe:Execution:\n        worker_group: g\n", "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || h.Service != nil || h.WorkerGroup != "g" {
		t.Errorf("step hints = %+v, want worker group g and no service", h)
	}
	if h, err := extractStepHints(nil, nil); h != nil || err != nil {
		t.Errorf("extractStepHints(nil, nil) = (%+v, %v)", h, err)
	}
}

// JSON-decoded numbers (float64) are accepted like positiveIntValue does for
// gpu_count.
func TestExtractStepHints_ServiceJSONNumbers(t *testing.T) {
	h, err := extractStepHints(map[string]any{"gowe:Service": map[string]any{
		"port_input": "port",
		"stop_grace": float64(45),
		"health":     map[string]any{"start_timeout": float64(120), "interval": int64(7), "failure_threshold": uint64(2)},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := h.Service
	if s.StopGraceSeconds != 45 || s.Health.StartTimeoutSeconds != 120 || s.Health.IntervalSeconds != 7 || s.Health.FailureThreshold != 2 {
		t.Errorf("service = %+v", s)
	}
}

func TestParser_ServiceHintErrors(t *testing.T) {
	tests := []struct {
		name      string
		toolHints string
		toolReqs  string
		stepHints string
		wantErr   string
	}{
		{name: "no port_input", toolHints: svcBody("scheme: http"), wantErr: "gowe:Service: port_input is required"},
		{name: "empty body", toolHints: "      gowe:Service:\n", wantErr: "port_input is required"},
		{name: "port_input not an input", toolHints: svcBody("port_input: nope"), wantErr: `port_input "nope" is not an input of tool "srv"`},
		{name: "port_input not int", toolHints: svcBody("port_input: name"), wantErr: `must name an input of type int, it is "string"`},
		{name: "step port_input not int", stepHints: "          gowe:Service:\n            port_input: name\n",
			wantErr: `step s1: gowe:Service: port_input "name" must name an input of type int`},
		{name: "bad scheme", toolHints: svcBody("port_input: port", "scheme: grpc"), wantErr: "gowe:Service: scheme must be http or https"},
		{name: "stop_grace too large", toolHints: svcBody("port_input: port", "stop_grace: 601"), wantErr: "gowe:Service: stop_grace must be between 1 and 600"},
		{name: "stop_grace zero", toolHints: svcBody("port_input: port", "stop_grace: 0"), wantErr: "stop_grace must be an integer >= 1"},
		{name: "start_timeout too large", toolHints: svcBody("port_input: port", "health:", "  start_timeout: 3601"),
			wantErr: "health.start_timeout must be between 1 and 3600"},
		{name: "interval too large", toolHints: svcBody("port_input: port", "health:", "  interval: 301"),
			wantErr: "health.interval must be between 1 and 300"},
		{name: "failure_threshold too large", toolHints: svcBody("port_input: port", "health:", "  failure_threshold: 101"),
			wantErr: "health.failure_threshold must be between 1 and 100"},
		{name: "relative path", toolHints: svcBody("port_input: port", "health:", "  path: health"),
			wantErr: `health.path must start with "/"`},
		{name: "path with space", toolHints: svcBody("port_input: port", "health:", `  path: "/a b"`),
			wantErr: "health.path must contain only printable ASCII"},
		{name: "path starting with //", toolHints: svcBody("port_input: port", "health:", `  path: "//h/health"`),
			wantErr: `health.path must not start with "//"`},
		{name: "path with fragment", toolHints: svcBody("port_input: port", "health:", `  path: "/health#x"`),
			wantErr: `health.path must not contain "#"`},
		{name: "ready_contains with control", toolHints: svcBody("port_input: port", "health:", "  ready_path: /r", `  ready_contains: "a\tb"`),
			wantErr: "health.ready_contains must not contain control characters"},
		{name: "empty port_input", toolHints: svcBody(`port_input: ""`), wantErr: "gowe:Service: port_input must not be empty"},
		{name: "empty scheme", toolHints: svcBody("port_input: port", `scheme: ""`), wantErr: "gowe:Service: scheme must not be empty"},
		{name: "empty health.path", toolHints: svcBody("port_input: port", "health:", `  path: ""`),
			wantErr: "gowe:Service: health.path must not be empty"},
		{name: "empty health.ready_path", toolHints: svcBody("port_input: port", "health:", `  ready_path: ""`),
			wantErr: "gowe:Service: health.ready_path must not be empty"},
		{name: "ready_contains without ready_path", toolHints: svcBody("port_input: port", "health:", "  ready_contains: x"),
			wantErr: "health.ready_contains requires health.ready_path"},
		{name: "checksum with space", toolHints: svcBody("port_input: port", `weights_checksum: "sha256: x"`),
			wantErr: "weights_checksum must contain only printable ASCII"},
		{name: "checksum non-ASCII", toolHints: svcBody("port_input: port", `weights_checksum: "sha256:ü"`),
			wantErr: "weights_checksum must contain only printable ASCII"},
		{name: "unknown key", toolHints: svcBody("port_input: port", "stop_grase: 5"), wantErr: `gowe:Service: unknown key "stop_grase"`},
		{name: "unknown health key", toolHints: svcBody("port_input: port", "health:", "  pth: /x"), wantErr: `unknown key "health.pth"`},
		{name: "port_input wrong type", toolHints: svcBody("port_input: 8080"), wantErr: "port_input must be a string, got int"},
		{name: "scheme wrong type", toolHints: svcBody("port_input: port", "scheme: [http]"), wantErr: "scheme must be a string"},
		{name: "interval string", toolHints: svcBody("port_input: port", "health:", `  interval: "10"`),
			wantErr: "health.interval must be an integer >= 1"},
		{name: "stop_grace expression", toolHints: svcBody("port_input: port", "stop_grace: $(inputs.n)"),
			wantErr: "stop_grace must be an integer >= 1"},
		{name: "health not a mapping", toolHints: svcBody("port_input: port", "health: /health"), wantErr: "health must be a mapping"},
		{name: "not a mapping", toolHints: "      gowe:Service: port\n", wantErr: "gowe:Service: must be a mapping"},
		{name: "hints and requirements", toolHints: svcBody("port_input: port"), toolReqs: svcBody("port_input: port"),
			wantErr: "declared under both hints and requirements"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := modelStepHints(t, serviceGraphDoc(tt.toolHints, tt.toolReqs, tt.stepHints, ""))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if !errors.Is(err, ErrInvalidServiceHint) {
				t.Errorf("err %v does not wrap ErrInvalidServiceHint", err)
			}
		})
	}
}

// gowe:Service is accepted only on a CommandLineTool run by a top-level
// step (or on that step).
func TestParser_ServicePlacement(t *testing.T) {
	const svcTool = `  - id: srv
    class: CommandLineTool
    baseCommand: [serve]
    inputs:
      port: int
    outputs:
      out:
        type: string
    hints:
      gowe:Service:
        port_input: port
`
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{name: "tool inside a sub-workflow", doc: `cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      port: int
    outputs: []
    steps:
      outer:
        run:
          class: Workflow
          inputs:
            port: int
          outputs: []
          steps:
            inner:
              run: "#srv"
              in:
                port: port
              out: [out]
        in:
          port: port
        out: []
` + svcTool, wantErr: "tool srv is not run by a step of the top-level workflow"},
		{name: "tool run by no step", doc: `cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs: []
    outputs: []
    steps: {}
` + svcTool, wantErr: "tool srv is not run by a step of the top-level workflow"},
		{name: "step inside a sub-workflow", doc: `cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      port: int
    outputs: []
    steps:
      outer:
        run:
          class: Workflow
          inputs:
            port: int
          outputs: []
          steps:
            inner:
              run:
                class: CommandLineTool
                baseCommand: [serve]
                inputs:
                  port: int
                outputs: []
              hints:
                gowe:Service:
                  port_input: port
              in:
                port: port
              out: []
        in:
          port: port
        out: []
`, wantErr: "step inner of sub-workflow"},
		{name: "on the workflow", doc: `cwlVersion: v1.2
class: Workflow
hints:
  gowe:Service:
    port_input: port
inputs:
  port: int
outputs: []
steps:
  s1:
    run:
      class: CommandLineTool
      baseCommand: [serve]
      inputs:
        port: int
      outputs: []
    in:
      port: port
    out: []
`, wantErr: "declared on the workflow"},
		{name: "on an ExpressionTool", doc: `cwlVersion: v1.2
class: ExpressionTool
requirements:
  InlineJavascriptRequirement: {}
hints:
  gowe:Service:
    port_input: port
inputs:
  port: int
outputs:
  out: int
expression: "$({out: inputs.port})"
`, wantErr: "declared on ExpressionTool"},
		{name: "step runs a sub-workflow", doc: `cwlVersion: v1.2
class: Workflow
inputs:
  port: int
outputs: []
steps:
  s1:
    run:
      class: Workflow
      inputs:
        port: int
      outputs: []
      steps: {}
    hints:
      gowe:Service:
        port_input: port
    in:
      port: port
    out: []
`, wantErr: "a service must be a CommandLineTool of this document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testParser()
			graph, err := p.ParseGraph([]byte(tt.doc))
			if err != nil {
				t.Fatalf("ParseGraph: %v", err)
			}
			_, err = p.ToModel(graph, "w")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if !errors.Is(err, ErrInvalidServiceHint) {
				t.Errorf("err %v does not wrap ErrInvalidServiceHint", err)
			}
		})
	}
}

// nestedServiceDoc is a bare workflow: top-level step a runs inline tool
// topID (no hint); step b runs inline sub-workflow inner, whose step s has
// the run: mapping inner (YAML, indented for step s).
func nestedServiceDoc(topID, inner string) string {
	return `cwlVersion: v1.2
class: Workflow
inputs:
  port: int
outputs: []
steps:
  a:
    run:
      class: CommandLineTool
      id: "` + topID + `"
      baseCommand: [echo]
      inputs:
        port: int
      outputs: []
    in:
      port: port
    out: []
  b:
    run:
      class: Workflow
      id: inner
      inputs:
        port: int
      outputs: []
      steps:
        s:
          run:
` + inner + `          in:
            port: port
          out: []
    in:
      port: port
    out: []
`
}

// nestedSvcTool is an inline service tool with id, indented as the run: of
// nestedServiceDoc's step s.
func nestedSvcTool(id string) string {
	return `            class: CommandLineTool
            id: "` + id + `"
            baseCommand: [serve]
            inputs:
              port: int
            outputs: []
            hints:
              gowe:Service:
                port_input: port
`
}

// A service tool anywhere inside a sub-workflow is refused whatever its id
// and however deep, without matching it through step references: a decoy
// top-level tool svc (no hint) beside a nested tool svc.cwl, a nested id
// starting with "#", and a tool two sub-workflows down.
func TestParser_ServiceNestedRefused(t *testing.T) {
	depth2 := `            class: Workflow
            id: leaf
            inputs:
              port: int
            outputs: []
            steps:
              t:
                run:
                  class: CommandLineTool
                  id: srv
                  baseCommand: [serve]
                  inputs:
                    port: int
                  outputs: []
                  hints:
                    gowe:Service:
                      port_input: port
                in:
                  port: port
                out: []
`
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"decoy top-level svc, nested svc.cwl", nestedServiceDoc("svc", nestedSvcTool("svc.cwl")), "tool svc.cwl of sub-workflow inner"},
		{"nested id starting with #", nestedServiceDoc("other", nestedSvcTool("#svc")), "tool #svc of sub-workflow inner"},
		{"nested id starting with #, decoy svc", nestedServiceDoc("svc", nestedSvcTool("#svc")), "tool #svc of sub-workflow inner"},
		{"depth 2", nestedServiceDoc("other", depth2), "tool srv of sub-workflow leaf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testParser()
			graph, err := p.ParseGraph([]byte(tt.doc))
			if err != nil {
				t.Fatalf("ParseGraph: %v", err)
			}
			_, err = p.ToModel(graph, "w")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) ||
				!strings.Contains(err.Error(), "a service cannot run inside a sub-workflow") {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
			if !errors.Is(err, ErrInvalidServiceHint) {
				t.Errorf("err %v does not wrap ErrInvalidServiceHint", err)
			}
		})
	}
}

// The top-level check uses ToModel's own lookup (graph.Tools[run without one
// leading "#"]): a top-level inline tool whose id starts with "#" is found
// and its service accepted and attached.
func TestParser_ServiceTopLevelHashID(t *testing.T) {
	doc := `cwlVersion: v1.2
class: Workflow
inputs:
  port: int
outputs: []
steps:
  s:
    run:
      class: CommandLineTool
      id: "#srv"
      baseCommand: [serve]
      inputs:
        port: int
      outputs: []
      hints:
        gowe:Service:
          port_input: port
    in:
      port: port
    out: []
`
	h, err := modelStepHints(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil || h.Service == nil || *h.Service != defaultService() {
		t.Fatalf("step hints = %+v, want the default service", h)
	}
}

// A key that is not literally gowe:Service but looks meant as it is refused
// (naming the key and the accepted spelling) wherever the hint is read; a
// :Service key of another namespace is left alone.
func TestParser_ServiceNearMissKey(t *testing.T) {
	nearTool := func(key string) string { return "      " + key + ":\n        port_input: port\n" }
	nearStep := func(key string) string { return "          " + key + ":\n            port_input: port\n" }
	withNS := func(doc, prefix, uri string) string {
		return strings.Replace(doc, "cwlVersion: v1.2\n", "cwlVersion: v1.2\n$namespaces:\n  "+prefix+": "+uri+"\n", 1)
	}
	const goweURI = "https://github.com/wilke/GoWe#"
	onWorkflow := `cwlVersion: v1.2
class: Workflow
hints:
  Gowe:Service:
    port_input: port
inputs:
  port: int
outputs: []
steps:
  s:
    run:
      class: CommandLineTool
      baseCommand: [serve]
      inputs:
        port: int
      outputs: []
    in:
      port: port
    out: []
`
	tests := []struct {
		name    string
		doc     string
		wantKey string // "" = no error
	}{
		{"lower case on the tool", serviceGraphDoc(nearTool("gowe:service"), "", "", ""), "gowe:service"},
		{"upper case in tool requirements", serviceGraphDoc("", nearTool("GOWE:SERVICE"), "", ""), "GOWE:SERVICE"},
		{"full URI on the tool", serviceGraphDoc(nearTool(`"`+goweURI+`Service"`), "", "", ""), goweURI + "Service"},
		{"other URI ending in #Service", serviceGraphDoc(nearTool(`"http://example.org/ns#Service"`), "", "", ""), "http://example.org/ns#Service"},
		{"prefix mapped to the gowe URI", withNS(serviceGraphDoc(nearTool("g:Service"), "", "", ""), "g", goweURI), "g:Service"},
		{"on the step", serviceGraphDoc("", "", nearStep("gowe:SERVICE"), ""), "gowe:SERVICE"},
		{"in step requirements", serviceGraphDoc("", "", "", nearStep("gowe:service")), "gowe:service"},
		{"on the workflow", onWorkflow, "Gowe:Service"},
		{"inside a sub-workflow", nestedServiceDoc("other", strings.Replace(nestedSvcTool("x"), "gowe:Service", "gowe:service", 1)), "gowe:service"},
		{"prefix of another namespace", withNS(serviceGraphDoc(nearTool("o:Service"), "", "", ""), "o", "http://example.org/o#"), ""},
		{"unmapped prefix", serviceGraphDoc(nearTool("o:Service"), "", "", ""), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := testParser()
			graph, err := p.ParseGraph([]byte(tt.doc))
			if err != nil {
				t.Fatalf("ParseGraph: %v", err)
			}
			_, err = p.ToModel(graph, "w")
			if tt.wantKey == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			want := `key "` + tt.wantKey + `" is not recognized; write it exactly "gowe:Service"`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want one containing %q", err, want)
			}
			if !errors.Is(err, ErrInvalidServiceHint) {
				t.Errorf("err %v does not wrap ErrInvalidServiceHint", err)
			}
		})
	}
}

// A bare CommandLineTool with the hint parses into a one-step workflow
// whose step carries the service.
func TestParser_BareToolService(t *testing.T) {
	doc := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: [serve]
inputs:
  port:
    type: int
    inputBinding:
      prefix: --port
outputs: []
hints:
  DockerRequirement:
    dockerPull: vllm.sif
  gowe:Execution:
    worker_group: gpu
  gowe:Service:
    port_input: port
`
	p := testParser()
	graph, err := p.ParseGraph([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	mw, err := p.ToModel(graph, "w")
	if err != nil {
		t.Fatal(err)
	}
	group, isService, err := model.ValidateServiceWorkflow(mw)
	if err != nil || !isService || group != "gpu" {
		t.Fatalf("ValidateServiceWorkflow = (%q, %v, %v), want (gpu, true, nil)", group, isService, err)
	}
	if want := defaultService(); *mw.Steps[0].Hints.Service != want {
		t.Errorf("service = %+v, want %+v", *mw.Steps[0].Hints.Service, want)
	}
}
