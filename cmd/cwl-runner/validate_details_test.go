package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// cwl-runner validate must show which field failed, not just
// "VALIDATION_ERROR: CWL validation failed" (#282).
func TestValidateCmdReportsFieldErrors(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"clt.cwl": "cwlVersion: v1.2\nclass: CommandLineTool\nbaseCommand: echo\n" +
			"inputs:\n  x: {type: string, inputBinding: {position: 1}}\noutputs:\n  y: stdout\n",
		"wf.cwl": "cwlVersion: v1.2\nclass: Workflow\ninputs: {x: string}\n" +
			"steps:\n  s: {run: clt.cwl, in: {x: x}, out: [y]}\n" +
			"outputs:\n  y: {type: Any, outputSource: s/z}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := validateCmd()
	cmd.SetArgs([]string{filepath.Join(dir, "wf.cwl")})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a validation error")
	}
	var details bytes.Buffer
	model.WriteErrorDetails(&details, err)
	want := `outputs.y.outputSource: outputSource "s/z" does not match any step output or workflow input`
	if !strings.Contains(details.String(), want) {
		t.Errorf("details = %q, want it to contain %q", details.String(), want)
	}
}
