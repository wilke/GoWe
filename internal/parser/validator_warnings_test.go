package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const warnTool = `cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  threads: {type: int, default: 1, inputBinding: {position: 1}}
outputs:
  y: stdout
`

const warnExprTool = `cwlVersion: v1.2
class: ExpressionTool
requirements: {InlineJavascriptRequirement: {}}
inputs: {threads: int}
outputs: {y: int}
expression: '$({y: inputs.threads})'
`

func warnWorkflow(stepIn string, extra string) string {
	return "cwlVersion: v1.2\nclass: Workflow\n" +
		"requirements: {StepInputExpressionRequirement: {}, InlineJavascriptRequirement: {}}\n" +
		"inputs: {n: int}\nsteps:\n  s:\n    run: clt.cwl\n" + extra +
		"    in:\n" + stepIn + "    out: [y]\noutputs:\n  y: {type: Any, outputSource: s/y}\n"
}

func TestValidatorWarnings_UndeclaredStepInputs(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		stepIn   string
		extra    string
		wantWarn []string // fields expected in Warnings, in order
	}{
		{name: "declared input", tool: warnTool,
			stepIn: "      threads: n\n"},
		{name: "misspelled input", tool: warnTool,
			stepIn:   "      treads: n\n",
			wantWarn: []string{"steps.s.in.treads"}},
		{name: "two unused inputs, sorted", tool: warnTool,
			stepIn:   "      zz: n\n      aa: n\n",
			wantWarn: []string{"steps.s.in.aa", "steps.s.in.zz"}},
		{name: "read by another input's valueFrom", tool: warnTool,
			stepIn: "      helper: n\n      threads: {valueFrom: '$(inputs.helper * 2)'}\n"},
		{name: "read with bracket syntax", tool: warnTool,
			stepIn: "      helper: n\n      threads: {valueFrom: '$(inputs[\"helper\"])'}\n"},
		{name: "read only by its own valueFrom", tool: warnTool,
			stepIn:   "      helper: {source: n, valueFrom: '$(inputs.helper)'}\n",
			wantWarn: []string{"steps.s.in.helper"}},
		{name: "prefix of a used name is still unused", tool: warnTool,
			stepIn:   "      help: n\n      helper: n\n      threads: {valueFrom: '$(inputs.helper)'}\n",
			wantWarn: []string{"steps.s.in.help"}},
		{name: "read by when", tool: warnTool,
			stepIn: "      run_it: {default: true}\n      threads: n\n",
			extra:  "    when: $(inputs.run_it)\n"},
		{name: "expression tool", tool: warnExprTool,
			stepIn:   "      threads: n\n      treads: n\n",
			wantWarn: []string{"steps.s.in.treads"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			wf := warnWorkflow(tt.stepIn, tt.extra)
			for name, content := range map[string]string{"wf.cwl": wf, "clt.cwl": tt.tool} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			graph, err := testParser().ParseGraphWithBase([]byte(wf), dir)
			if err != nil {
				t.Fatalf("ParseGraphWithBase: %v", err)
			}
			v := testValidator()
			if verr := v.Validate(graph); verr != nil {
				t.Fatalf("Validate must accept undeclared step inputs: %v %+v", verr, verr.Details)
			}
			warns := v.Warnings(graph)
			var got []string
			for _, w := range warns {
				got = append(got, w.Field)
				if !strings.Contains(w.Message, "is ignored") {
					t.Errorf("message %q does not say the input is ignored", w.Message)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.wantWarn, ",") {
				t.Errorf("warnings = %v, want %v", got, tt.wantWarn)
			}
		})
	}
}

// The required conformance test wf_step_connect_undeclared_param connects an
// undeclared input: it must stay valid, with a warning.
func TestValidatorWarnings_ConformancePassUnconnected(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "cwl-v1.2", "tests")
	data, err := os.ReadFile(filepath.Join(dir, "pass-unconnected.cwl"))
	if err != nil {
		t.Skipf("conformance test data not present: %v", err)
	}
	graph, err := testParser().ParseGraphWithBase(data, dir)
	if err != nil {
		t.Fatalf("ParseGraphWithBase: %v", err)
	}
	v := testValidator()
	if verr := v.Validate(graph); verr != nil {
		t.Fatalf("Validate: %v %+v", verr, verr.Details)
	}
	warns := v.Warnings(graph)
	if len(warns) != 1 || warns[0].Field != "steps.step1.in.in2" {
		t.Errorf("warnings = %+v, want one for steps.step1.in.in2", warns)
	}
}
