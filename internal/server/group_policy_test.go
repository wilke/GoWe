package server

import (
	"context"
	"encoding/json"
	"net/http"
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

// gpPolicy stores a rule for group gpu (secrets-tester and admins) and loads
// it into a new policy with adminCfg's admin lookup.
func gpPolicy(t *testing.T, adminCfg *AdminConfig) *groupacl.Policy {
	t.Helper()
	ctx := context.Background()
	st := testAuthStore(t)
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"secrets-tester"}, Admins: true}); err != nil {
		t.Fatal(err)
	}
	p := groupacl.New().WithAdmins(adminCfg.IsAdmin)
	if err := p.Reload(ctx, st); err != nil {
		t.Fatal(err)
	}
	return p
}

// A change made through the admin API applies to the next submission, with
// no restart.
func TestCreateSubmission_GroupPolicyChangeAppliesAtOnce(t *testing.T) {
	srv, _, _ := newWorkerGroupServer(t)
	wfID, _ := tcRegister(t, srv, gpDoc(""))
	body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": map[string]any{}, "labels": map[string]string{"worker_group": "gpu"}})
	submit := func() *envelope {
		t.Helper()
		w, env := doPostAs(t, srv, "/api/v1/submissions", string(body), userAuthToken)
		if w.Code != http.StatusCreated && w.Code != http.StatusForbidden {
			t.Fatalf("submit: %d %s", w.Code, w.Body.String())
		}
		return &env
	}

	if env := submit(); env.Error != nil {
		t.Fatalf("no rule yet: submission refused: %+v", env.Error)
	}
	if w, _ := doPutAs(t, srv, workerGroupsPath+"/gpu", `{"users":["secrets-tester"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT gpu: %d %s", w.Code, w.Body.String())
	}
	if env := submit(); env.Error == nil || env.Error.Message != `user "tester" may not use worker group "gpu"` {
		t.Fatalf("after the rule: error = %+v, want the 403 for tester", env.Error)
	}
	if w := doDeleteAs(t, srv, workerGroupsPath+"/gpu", adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("DELETE gpu: %d %s", w.Code, w.Body.String())
	}
	if env := submit(); env.Error != nil {
		t.Fatalf("after deleting the rule: submission refused: %+v", env.Error)
	}
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
