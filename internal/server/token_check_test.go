package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

const tcTool = `  - id: tok
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      gowe:Execution:
        executor: worker
        inject_bvbrc_token: true
    inputs: {}
    outputs:
      out:
        type: string
`

func tcDoc(wfExtra, stepExtra string) string {
	return "cwlVersion: v1.2\n$graph:\n" + tcTool + `  - id: main
    class: Workflow
` + wfExtra + `    inputs: {}
    outputs: {}
    steps:
      s:
        run: "#tok"
        in: {}
        out: [out]
` + stepExtra
}

// nested: main -> sub (inline-by-reference sub-workflow) -> inner token step.
func tcNestedDoc(parentExtra string) string {
	return "cwlVersion: v1.2\n$graph:\n" + tcTool + `  - id: subwf
    class: Workflow
    inputs: {}
    outputs: {}
    steps:
      inner:
        run: "#tok"
        in: {}
        out: [out]
  - id: main
    class: Workflow
    requirements:
      SubworkflowFeatureRequirement: {}
` + parentExtra + `    inputs: {}
    outputs: {}
    steps:
      sub:
        run: "#subwf"
        in: {}
        out: [out]
`
}

func tcRegister(t *testing.T, srv *Server, raw string) (string, []string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "tc-" + time.Now().Format("150405.000000"), "cwl": raw})
	w, env := doPostAs(t, srv, "/api/v1/workflows", string(body), secretsTestToken)
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	var wf struct {
		ID       string   `json:"id"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(env.Data, &wf); err != nil {
		t.Fatal(err)
	}
	return wf.ID, wf.Warnings
}

func tcSubmit(t *testing.T, srv *Server, wfID string, inputs map[string]any) (int, envelope) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": inputs})
	w, env := doPostAs(t, srv, "/api/v1/submissions", string(body), secretsTestToken)
	return w.Code, env
}

func tcBless(t *testing.T, st store.Store, raw, stepPath string, groups []string) {
	t.Helper()
	res, err := toolhash.DigestForStep(raw, stepPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToolBlessing(context.Background(), &model.ToolBlessing{
		ID: "tb_" + strings.ReplaceAll(stepPath, "/", "_"), ToolHash: res.Hash, ToolName: "tok",
		ImageRef: "python.sif", ImageSHA256s: []string{shaA}, WorkerGroups: groups,
		BlessedBy: "admin", BlessedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateSubmission_UnblessedTokenRequest_400(t *testing.T) {
	srv, st := testServerWithStore()
	raw := tcDoc("", "")
	wfID, warns := tcRegister(t, srv, raw)
	if len(warns) == 0 || !strings.Contains(warns[0], "not blessed") {
		t.Errorf("registration should warn about the unblessed tool: %v", warns)
	}

	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 400 || env.Error == nil || env.Error.Code != ErrToolNotBlessed {
		t.Fatalf("got %d %+v", code, env.Error)
	}
	res, _ := toolhash.DigestForStep(raw, "s", nil)
	for _, want := range []string{`step "s"`, `tool "tok"`, res.Hash} {
		if !strings.Contains(env.Error.Message, want) {
			t.Errorf("message %q lacks %q", env.Error.Message, want)
		}
	}
	if len(env.Error.Details) != 1 || env.Error.Details[0].Field != "s" || env.Error.Details[0].Path != res.Hash {
		t.Errorf("details = %+v", env.Error.Details)
	}

	// Blessed -> accepted; blessed but wrong group -> refused.
	tcBless(t, st, raw, "s", nil)
	if code, env := tcSubmit(t, srv, wfID, map[string]any{}); code != 201 {
		t.Fatalf("blessed submission: %d %+v", code, env.Error)
	}
}

func TestCreateSubmission_BlessedButGroupDenied_400(t *testing.T) {
	srv, st := testServerWithStore()
	raw := tcDoc("", "")
	wfID, _ := tcRegister(t, srv, raw)
	tcBless(t, st, raw, "s", []string{"esmfold"})
	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 400 || env.Error == nil || !strings.Contains(env.Error.Message, `worker group "default"`) {
		t.Fatalf("got %d %+v", code, env.Error)
	}
}

func TestCreateSubmission_CwlRequirementsRefused_400(t *testing.T) {
	srv, st := testServerWithStore()
	raw := tcDoc("", "")
	wfID, _ := tcRegister(t, srv, raw)
	tcBless(t, st, raw, "s", nil)
	code, env := tcSubmit(t, srv, wfID, map[string]any{
		"cwl:requirements": []any{map[string]any{"class": "EnvVarRequirement", "envDef": []any{map[string]any{"envName": "X", "envValue": "1"}}}},
	})
	if code != 400 || env.Error == nil || env.Error.Code != ErrTokenToolOverrideRefused {
		t.Fatalf("got %d %+v", code, env.Error)
	}
}

func TestCreateSubmission_StepImageInRequirementsOrHintsRefused_400(t *testing.T) {
	for name, stepExtra := range map[string]string{
		"requirements": "        requirements:\n          DockerRequirement:\n            dockerPull: evil.sif\n",
		"hints":        "        hints:\n          DockerRequirement:\n            dockerPull: evil.sif\n",
		"gowe-hint":    "        hints:\n          gowe:Execution:\n            docker_image: evil.sif\n",
	} {
		t.Run(name, func(t *testing.T) {
			srv, st := testServerWithStore()
			// step keys must sit inside the step map: insert after "out: [out]".
			raw := strings.Replace(tcDoc("", ""), "        out: [out]\n", "        out: [out]\n"+stepExtra, 1)
			wfID, _ := tcRegister(t, srv, raw)
			// even a blessing of whatever the step hashes to cannot override the refusal
			tcBless(t, st, raw, "s", nil)
			code, env := tcSubmit(t, srv, wfID, map[string]any{})
			if code != 400 || env.Error == nil || env.Error.Code != ErrTokenToolOverrideRefused {
				t.Fatalf("got %d %+v", code, env.Error)
			}
		})
	}
}

// A workflow that adds a non-benign requirement to a blessed tool changes the
// hash; the error names the inherited class.
func TestCreateSubmission_InheritedEnvVarRequirementNamesClass(t *testing.T) {
	srv, st := testServerWithStore()
	plain := tcDoc("", "")
	tcBless(t, st, plain, "s", nil)
	withEnv := tcDoc("    requirements:\n      EnvVarRequirement:\n        envDef:\n          - envName: X\n            envValue: \"1\"\n", "")
	wfID, _ := tcRegister(t, srv, withEnv)
	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 400 || env.Error == nil || env.Error.Code != ErrToolNotBlessed || !strings.Contains(env.Error.Message, "EnvVarRequirement") {
		t.Fatalf("got %d %+v", code, env.Error)
	}
}

// Nested steps are checked against the sub-workflow CHILD root, exactly as
// the scheduler builds it: parent-level requirements do not reach them, so a
// blessing of the child-root digest is accepted, and an unblessed nested step
// is reported with its full path.
func TestCreateSubmission_NestedStepChecked_ChildRoot(t *testing.T) {
	srv, st := testServerWithStore()
	raw := tcNestedDoc("    hints:\n      EnvVarRequirement:\n        envDef:\n          - envName: P\n            envValue: parent\n")
	wfID, _ := tcRegister(t, srv, raw)

	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 400 || env.Error == nil || env.Error.Code != ErrToolNotBlessed || env.Error.Details[0].Field != "sub/inner" {
		t.Fatalf("unblessed nested: %d %+v", code, env.Error)
	}

	tcBless(t, st, raw, "sub/inner", nil)
	if code, env := tcSubmit(t, srv, wfID, map[string]any{}); code != 201 {
		t.Fatalf("blessed nested (parent hints must not be inherited): %d %+v", code, env.Error)
	}
}

func TestCreateSubmission_DevMode_AcceptsUnblessedWithWarning(t *testing.T) {
	srv, _ := testServerWithStore(WithTokenBlessingMode("dev"))
	wfID, _ := tcRegister(t, srv, tcDoc("", ""))
	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 201 {
		t.Fatalf("dev mode must accept: %d %+v", code, env.Error)
	}
	var resp struct {
		Warnings []model.FieldError `json:"warnings"`
	}
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Warnings) != 1 || !strings.Contains(resp.Warnings[0].Message, `tool "tok"`) || !strings.Contains(resp.Warnings[0].Message, "NOT blessed") {
		t.Errorf("warnings = %+v", resp.Warnings)
	}
}

func TestHealth_ReportsTokenBlessingMode(t *testing.T) {
	for mode, want := range map[string]string{"": "enforce", "enforce": "enforce", "dev": "dev"} {
		srv := testServer(WithTokenBlessingMode(mode))
		w, env := doGetAs(t, srv, "/api/v1/health", secretsTestToken)
		var h struct {
			TokenBlessing string `json:"token_blessing"`
		}
		_ = json.Unmarshal(env.Data, &h)
		if w.Code != 200 || h.TokenBlessing != want {
			t.Errorf("mode %q: %d token_blessing=%q want %q", mode, w.Code, h.TokenBlessing, want)
		}
	}
}

func TestRegisterWorkflow_GoweExecutionUnderRequirementsRejected(t *testing.T) {
	srv := testServer()
	raw := strings.Replace(tcDoc("", ""), "    hints:\n      gowe:Execution:", "    requirements:\n      gowe:Execution:", 1)
	// two "requirements:" keys would collide; drop the DockerRequirement block's parent instead.
	raw = strings.Replace(tcDoc("", ""), "    hints:\n      gowe:Execution:\n        executor: worker\n        inject_bvbrc_token: true\n",
		"      gowe:Execution:\n        executor: worker\n        inject_bvbrc_token: true\n", 1)
	body, _ := json.Marshal(map[string]any{"name": "bad", "cwl": raw})
	w, env := doPostAs(t, srv, "/api/v1/workflows", string(body), secretsTestToken)
	if w.Code != 400 || env.Error == nil || !strings.Contains(env.Error.Message, "only honoured under hints") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
