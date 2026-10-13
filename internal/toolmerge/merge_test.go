package toolmerge

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	"gopkg.in/yaml.v3"
)

const fixtureCWL = `cwlVersion: v1.2
$graph:
  - id: tool-a
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      ResourceRequirement:
        coresMin: 1
    inputs:
      x: string
    outputs: {}
  - id: tool-b
    class: CommandLineTool
    baseCommand: [cat]
    hints:
      DockerRequirement:
        dockerPull: other.sif
    inputs: {}
    outputs: {}
  - id: inner
    class: Workflow
    requirements:
      EnvVarRequirement:
        envDef:
          INNER: "1"
    inputs: {}
    outputs: {}
    steps:
      deep:
        run: "#tool-b"
        in: {}
        out: []
  - id: main
    class: Workflow
    requirements:
      InlineJavascriptRequirement: {}
      EnvVarRequirement:
        envDef:
          FOO: bar
    hints:
      ResourceRequirement:
        ramMin: 100
      gowe:ResourceData:
        datasets: []
    inputs: {}
    outputs: {}
    steps:
      a:
        run: "#tool-a"
        requirements:
          NetworkAccess:
            networkAccess: true
        hints:
          DockerRequirement:
            dockerPull: step.sif
        in: {}
        out: []
      b:
        run: "#tool-b"
        in: {}
        out: []
      sub:
        run: "#inner"
        in: {}
        out: []
`

func parseFixture(t *testing.T, raw string) *cwl.GraphDocument {
	t.Helper()
	g, err := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(raw))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	return g
}

func toolMap(t *testing.T, g *cwl.GraphDocument, id string) map[string]any {
	t.Helper()
	m, err := StructToToolMap(g.Tools[id])
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMerge_ByteIdenticalToLegacy proves the extraction did not change the
// merge result: the extracted functions and the pre-extraction copies produce
// byte-identical JSON for every step of the fixture, with and without job
// cwl:requirements.
func TestMerge_ByteIdenticalToLegacy(t *testing.T) {
	g := parseFixture(t, fixtureCWL)
	jobReqSets := map[string][]any{
		"none":   nil,
		"docker": {map[string]any{"class": "DockerRequirement", "dockerPull": "job.sif"}},
		"env+junk": {
			map[string]any{"class": "EnvVarRequirement", "envDef": map[string]any{"J": "1"}},
			"not-a-map",
			map[string]any{"noclass": true},
		},
	}
	cases := []struct{ step, tool string }{{"a", "tool-a"}, {"b", "tool-b"}}
	for _, c := range cases {
		for name, jr := range jobReqSets {
			t.Run(c.step+"/"+name, func(t *testing.T) {
				got := toolMap(t, g, c.tool)
				want := toolMap(t, g, c.tool)
				MergeRequirementsIntoTool(got, g.Workflow, c.step)
				legacyMergeRequirementsIntoTool(want, g.Workflow, c.step)
				if len(jr) > 0 {
					MergeJobRequirementsIntoTool(got, jr)
					legacyMergeJobRequirementsIntoTool(want, jr)
				}
				if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
					t.Errorf("merged tool differs from legacy\n got: %s\nwant: %s", a, b)
				}
			})
		}
	}
	// nil workflow / nil tool are no-ops, as before.
	MergeRequirementsIntoTool(nil, g.Workflow, "a")
	MergeRequirementsIntoTool(map[string]any{}, nil, "a")
	MergeJobRequirementsIntoTool(nil, []any{map[string]any{"class": "X"}})
}

func TestMerge_Precedence(t *testing.T) {
	g := parseFixture(t, fixtureCWL)
	tool := toolMap(t, g, "tool-a")
	MergeRequirementsIntoTool(tool, g.Workflow, "a")
	reqs := tool["requirements"].(map[string]any)
	if dr := reqs["DockerRequirement"].(map[string]any)["dockerPull"]; dr != "python.sif" {
		t.Errorf("tool requirement must win over step hint, got %v", dr)
	}
	if _, ok := reqs["NetworkAccess"]; !ok {
		t.Error("step requirement NetworkAccess should be inherited")
	}
	if _, ok := reqs["EnvVarRequirement"]; !ok {
		t.Error("workflow requirement EnvVarRequirement should be inherited")
	}
	MergeJobRequirementsIntoTool(tool, []any{map[string]any{"class": "DockerRequirement", "dockerPull": "job.sif"}})
	if dr := tool["requirements"].(map[string]any)["DockerRequirement"].(map[string]any)["dockerPull"]; dr != "job.sif" {
		t.Errorf("job requirement must win, got %v", dr)
	}
}

func TestStructToToolMap_Deterministic(t *testing.T) {
	g := parseFixture(t, fixtureCWL)
	a := mustJSON(t, toolMap(t, g, "tool-a"))
	b := mustJSON(t, toolMap(t, g, "tool-a"))
	if a != b {
		t.Fatal("StructToToolMap must be deterministic")
	}
}

func TestChildGraphForStep(t *testing.T) {
	out, err := ChildGraphForStep(fixtureCWL, "sub")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	graph := doc["$graph"].([]any)
	var mains int
	for _, it := range graph {
		m := it.(map[string]any)
		if m["id"] == "main" {
			mains++
			if _, ok := m["requirements"].(map[string]any)["EnvVarRequirement"]; !ok {
				t.Error("child main must be the sub-workflow (INNER env)")
			}
			if strings.Contains(mustJSON(t, m), `"FOO"`) {
				t.Error("parent workflow requirements must not leak into the child root")
			}
		}
		if m["id"] == "inner" {
			t.Error("sub-workflow must not remain as a separate graph entry")
		}
	}
	if mains != 1 {
		t.Errorf("want exactly one main, got %d", mains)
	}
	if _, err := ChildGraphForStep(fixtureCWL, "a"); err == nil {
		t.Error("a tool step is not a sub-workflow; expected error")
	}
	if _, err := ChildGraphForStep(fixtureCWL, "nope"); err == nil {
		t.Error("missing step should error")
	}
}
