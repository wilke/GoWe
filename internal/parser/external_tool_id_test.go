package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tools loaded from external files must be keyed by file basename, whatever
// id the file declares: step.Run stays "<name>.cwl" and both the validator and
// cwlrunner resolve it to "<name>" (#281).

const extWorkflow = `cwlVersion: v1.2
class: Workflow
inputs: {x: string}
steps:
  s: {run: clt.cwl, in: {x: x}, out: [y]}
outputs:
  y: {type: Any, outputSource: s/y}
`

func extTool(idLine string) string {
	return "cwlVersion: v1.2\nclass: CommandLineTool\n" + idLine +
		"baseCommand: echo\ninputs:\n  x: {type: string, inputBinding: {position: 1}}\noutputs:\n  y: stdout\n"
}

func extExprTool(idLine string) string {
	return "cwlVersion: v1.2\nclass: ExpressionTool\n" + idLine +
		"requirements: {InlineJavascriptRequirement: {}}\ninputs: {x: string}\n" +
		"outputs: {y: string}\nexpression: '$({y: inputs.x})'\n"
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseGraphWithBase_ExternalToolID(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		wantTool string // key expected in graph.Tools (or ExpressionTools when expr)
		expr     bool
		wantID   string // declared id kept on the tool
	}{
		{
			name:     "no id",
			files:    map[string]string{"wf.cwl": extWorkflow, "clt.cwl": extTool("")},
			wantTool: "clt",
			wantID:   "clt",
		},
		{
			name:     "id equals basename",
			files:    map[string]string{"wf.cwl": extWorkflow, "clt.cwl": extTool("id: clt\n")},
			wantTool: "clt",
			wantID:   "clt",
		},
		{
			name:     "issue repro: id differs from basename",
			files:    map[string]string{"wf.cwl": extWorkflow, "clt.cwl": extTool("id: mytool\n")},
			wantTool: "clt",
			wantID:   "mytool",
		},
		{
			name:     "hash-prefixed id",
			files:    map[string]string{"wf.cwl": extWorkflow, "clt.cwl": extTool("id: '#mytool'\n")},
			wantTool: "clt",
		},
		{
			name:     "expression tool with differing id",
			files:    map[string]string{"wf.cwl": extWorkflow, "clt.cwl": extExprTool("id: myexpr\n")},
			wantTool: "clt",
			expr:     true,
			wantID:   "myexpr",
		},
		{
			name: "same file used by two steps",
			files: map[string]string{
				"wf.cwl": `cwlVersion: v1.2
class: Workflow
inputs: {x: string}
steps:
  a: {run: clt.cwl, in: {x: x}, out: [y]}
  b: {run: clt.cwl, in: {x: x}, out: [y]}
outputs:
  ya: {type: Any, outputSource: a/y}
  yb: {type: Any, outputSource: b/y}
`,
				"clt.cwl": extTool("id: mytool\n"),
			},
			wantTool: "clt",
			wantID:   "mytool",
		},
		{
			name: "tool in a subdirectory",
			files: map[string]string{
				"wf.cwl":        strings.Replace(extWorkflow, "run: clt.cwl", "run: tools/clt.cwl", 1),
				"tools/clt.cwl": extTool("id: mytool\n"),
			},
			wantTool: "clt",
			wantID:   "mytool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			graph, err := testParser().ParseGraphWithBase([]byte(tt.files["wf.cwl"]), dir)
			if err != nil {
				t.Fatalf("ParseGraphWithBase: %v", err)
			}
			if verr := testValidator().Validate(graph); verr != nil {
				t.Fatalf("Validate: %v %+v", verr, verr.Details)
			}
			if tt.expr {
				et, ok := graph.ExpressionTools[tt.wantTool]
				if !ok {
					t.Fatalf("ExpressionTools has no key %q (keys: %v)", tt.wantTool, keysOf(graph.ExpressionTools))
				}
				if tt.wantID != "" && et.ID != tt.wantID {
					t.Errorf("declared id = %q, want %q", et.ID, tt.wantID)
				}
				return
			}
			tool, ok := graph.Tools[tt.wantTool]
			if !ok {
				t.Fatalf("Tools has no key %q (keys: %v)", tt.wantTool, keysOf(graph.Tools))
			}
			if len(graph.Tools) != 1 {
				t.Errorf("len(Tools) = %d, want 1 (keys: %v)", len(graph.Tools), keysOf(graph.Tools))
			}
			if tt.wantID != "" && tool.ID != tt.wantID {
				t.Errorf("declared id = %q, want %q", tool.ID, tt.wantID)
			}
		})
	}
}

// The esviritu app validated only because its tool ids happened to equal their
// file names; giving pair-samples.cwl a different id used to fail (#281).
func TestParseGraphWithBase_EsvirituToolIDs(t *testing.T) {
	src := filepath.Join("..", "..", "cwl", "apps", "esviritu")
	for _, rename := range []bool{false, true} {
		name := "as committed"
		if rename {
			name = "pair-samples.cwl with id esviritu-pair-samples"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			entries, err := os.ReadDir(src)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".cwl") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(src, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if rename && e.Name() == "pair-samples.cwl" {
					data = []byte(strings.Replace(string(data),
						"class: CommandLineTool\n", "class: CommandLineTool\nid: esviritu-pair-samples\n", 1))
				}
				if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wf, err := os.ReadFile(filepath.Join(dir, "esviritu-run.cwl"))
			if err != nil {
				t.Fatal(err)
			}
			graph, err := testParser().ParseGraphWithBase(wf, dir)
			if err != nil {
				t.Fatalf("ParseGraphWithBase: %v", err)
			}
			if verr := testValidator().Validate(graph); verr != nil {
				t.Fatalf("Validate: %v %+v", verr, verr.Details)
			}
			if _, ok := graph.Tools["pair-samples"]; !ok {
				t.Errorf("Tools has no key pair-samples (keys: %v)", keysOf(graph.Tools))
			}
		})
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
