package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A step input the tool does not declare and nothing reads is legal CWL but
// usually a typo; registration accepts it and reports a warning, also on a
// dedup hit.
func TestCreateWorkflow_UndeclaredStepInputWarning(t *testing.T) {
	const packed = `cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    inputs:
      n: int
    outputs:
      y: {type: File, outputSource: s/y}
    steps:
      s:
        run: "#tool"
        in:
          treads: n
        out: [y]
  - id: tool
    class: CommandLineTool
    baseCommand: echo
    inputs:
      threads: {type: int, default: 1, inputBinding: {position: 1}}
    outputs:
      y: stdout
`
	srv := testServer()
	body, _ := json.Marshal(map[string]string{"name": "warn-wf", "cwl": packed})
	for _, attempt := range []string{"create", "dedup"} {
		w, env := doPost(t, srv, "/api/v1/workflows/", string(body))
		if w.Code != http.StatusCreated && w.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", attempt, w.Code, w.Body.String())
		}
		var data struct {
			Deduplicated bool     `json:"deduplicated"`
			Warnings     []string `json:"warnings"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatal(err)
		}
		if attempt == "dedup" && !data.Deduplicated {
			t.Errorf("second POST: deduplicated = false, want true")
		}
		if len(data.Warnings) != 1 || !strings.HasPrefix(data.Warnings[0], "steps.s.in.treads: ") {
			t.Errorf("%s: warnings = %q, want one for steps.s.in.treads", attempt, data.Warnings)
		}
	}
}
