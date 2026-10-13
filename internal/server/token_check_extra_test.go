package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCreateSubmission_TokenStepWithoutUserToken_400(t *testing.T) {
	srv, st := testServerWithStore()
	raw := tcDoc("", "")
	wfID, _ := tcRegister(t, srv, raw)
	tcBless(t, st, raw, "s", nil)
	body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": map[string]any{}})
	w, env := doPostAs(t, srv, "/api/v1/submissions", string(body), "") // no usable token
	if w.Code == 201 {
		t.Fatalf("a submission without a user token must not be accepted for a token-requesting step: %+v", env.Error)
	}
}

func TestCreateSubmission_TokenToolWithoutImage_400(t *testing.T) {
	srv, _ := testServerWithStore(WithTokenBlessingMode("dev"))
	raw := strings.Replace(tcDoc("", ""), "    requirements:\n      DockerRequirement:\n        dockerPull: python.sif\n", "", 1)
	wfID, _ := tcRegister(t, srv, raw)
	code, env := tcSubmit(t, srv, wfID, map[string]any{})
	if code != 400 || env.Error == nil || !strings.Contains(env.Error.Message, "no container image") {
		t.Fatalf("got %d %+v", code, env.Error)
	}
}
