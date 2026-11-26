package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/internal/groupacl"
	"github.com/me/gowe/pkg/model"
)

// Worker-group access fast-fail at submission (#261).

func gpDoc(stepHints string) string {
	return `cwlVersion: v1.2
$graph:
  - id: echo-tool
    class: CommandLineTool
    baseCommand: [echo]
    inputs: {}
    outputs:
      out:
        type: string
  - id: main
    class: Workflow
    inputs: {}
    outputs: {}
    steps:
      s:
        run: "#echo-tool"
` + stepHints + `        in: {}
        out: [out]
`
}

const gpStepHint = `        hints:
          gowe:Execution:
            worker_group: gpu
`

func gpPolicy(t *testing.T, adminCfg *AdminConfig) *groupacl.Policy {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.json")
	cfg := `{"admins": [], "worker_groups": {"groups": {"gpu": {"users": ["secrets-tester"], "admins": true}}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := groupacl.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p.WithAdmins(adminCfg.IsAdmin)
}

func TestCreateSubmission_GroupPolicy(t *testing.T) {
	adminCfg := NewAdminConfig(nil, "GOWE_TEST_ADMINS_UNSET_VAR", "")
	adminCfg.WithCLIAdmins([]string{"admin"})
	policy := gpPolicy(t, adminCfg)
	gpuLabel := map[string]string{"worker_group": "gpu"}

	tests := []struct {
		name      string
		policy    *groupacl.Policy
		stepHints string
		labels    map[string]string
		token     string
		wantCode  int
		wantMsg   string
	}{
		{"label: unlisted user 403", policy, "", gpuLabel, userAuthToken, http.StatusForbidden, `user "tester" may not use worker group "gpu"`},
		{"step hint: unlisted user 403", policy, gpStepHint, nil, userAuthToken, http.StatusForbidden, `user "tester" may not use worker group "gpu"`},
		{"label: listed user accepted", policy, "", gpuLabel, secretsTestToken, http.StatusCreated, ""},
		{"step hint: listed user accepted", policy, gpStepHint, nil, secretsTestToken, http.StatusCreated, ""},
		{"step hint: admin accepted (admins true)", policy, gpStepHint, nil, adminAuthToken, http.StatusCreated, ""},
		{"unlisted group open", policy, "", map[string]string{"worker_group": "esmfold"}, userAuthToken, http.StatusCreated, ""},
		{"no policy: label accepted", nil, "", gpuLabel, userAuthToken, http.StatusCreated, ""},
		{"no policy: step hint accepted", nil, gpStepHint, nil, userAuthToken, http.StatusCreated, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testServer(WithAdminConfig(adminCfg), WithGroupPolicy(tt.policy))
			wfID, _ := tcRegister(t, srv, gpDoc(tt.stepHints))
			body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": map[string]any{}, "labels": tt.labels})
			w, env := doPostAs(t, srv, "/api/v1/submissions", string(body), tt.token)
			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.wantCode, w.Body.String())
			}
			if tt.wantMsg == "" {
				return
			}
			if env.Error == nil || env.Error.Code != model.ErrForbidden || env.Error.Message != tt.wantMsg {
				t.Errorf("error = %+v, want %s %q", env.Error, model.ErrForbidden, tt.wantMsg)
			}
		})
	}
}
