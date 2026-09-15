package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// secretsTestToken authenticates every secrets test as a stable, non-anonymous
// user ("secrets-tester"), so ownership checks (GET/DELETE/retry) and the
// anonymous-submission restriction behave predictably; the dedicated
// anonymous test below deliberately omits it (doPost with no Authorization
// header resolves to the anonymous user, same as every other unauthenticated
// helper in this package).
const secretsTestToken = "un=secrets-tester|tokenid=t-secrets|expiry=4102444800|sig=s"

func doPostAs(t *testing.T, srv *Server, path, body, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func doGetAs(t *testing.T, srv *Server, path, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

func doDeleteAs(t *testing.T, srv *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("DELETE", path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func doPutAs(t *testing.T, srv *Server, path, body, token string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	return w, env
}

// bodyContainsValue is a crude but effective non-echo check: the raw
// response body must not contain the literal secret value anywhere,
// regardless of which field it might have leaked through.
func bodyContainsValue(body []byte, value string) bool {
	return strings.Contains(string(body), value)
}

// forceSubmissionTerminal moves a submission directly to a terminal state
// via the store, bypassing the state-machine-guarded API transitions —
// standing in for "the scheduler eventually finished it" in tests that only
// care about post-terminal behavior (e.g. DELETE .../secrets, #260 H3).
func forceSubmissionTerminal(t *testing.T, st store.Store, id string, state model.SubmissionState) {
	t.Helper()
	sub, err := st.GetSubmission(context.Background(), id)
	if err != nil || sub == nil {
		t.Fatalf("get submission %s: %v", id, err)
	}
	sub.State = state
	now := time.Now().UTC()
	sub.CompletedAt = &now
	if err := st.UpdateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("force submission %s terminal: %v", id, err)
	}
}

func TestCreateSubmission_SecretsNeverEchoed(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"HF_TOKEN": "super-secret-value-123"},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	if bodyContainsValue(w.Body.Bytes(), "super-secret-value-123") {
		t.Fatalf("create response leaks secret value: %s", w.Body.String())
	}

	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if data["secrets_state"] != "present" {
		t.Errorf("secrets_state = %v, want present", data["secrets_state"])
	}
	names, ok := data["secret_names"].([]any)
	if !ok || len(names) != 1 || names[0] != "HF_TOKEN" {
		t.Errorf("secret_names = %v, want [HF_TOKEN]", data["secret_names"])
	}
	if _, has := data["secrets"]; has {
		t.Errorf("create response unexpectedly contains a 'secrets' field: %v", data["secrets"])
	}

	subID, _ := data["id"].(string)
	if subID == "" {
		t.Fatalf("created submission missing id, data=%v", data)
	}

	// GET must also never echo the value.
	getW, getEnv := doGetAs(t, srv, "/api/v1/submissions/"+subID, secretsTestToken)
	if getW.Code != http.StatusOK {
		t.Fatalf("get submission: status=%d, body=%s", getW.Code, getW.Body.String())
	}
	if bodyContainsValue(getEnv.Data, "super-secret-value-123") {
		t.Fatalf("GET response leaks secret value: %s", getEnv.Data)
	}
	var getData map[string]any
	json.Unmarshal(getEnv.Data, &getData)
	if getData["secrets_state"] != "present" {
		t.Errorf("GET secrets_state = %v, want present", getData["secrets_state"])
	}

	// List must also never echo the value.
	listW, listEnv := doGetAs(t, srv, "/api/v1/submissions/", secretsTestToken)
	if listW.Code != http.StatusOK {
		t.Fatalf("list submissions: status=%d, body=%s", listW.Code, listW.Body.String())
	}
	if bodyContainsValue(listEnv.Data, "super-secret-value-123") {
		t.Fatalf("list response leaks secret value: %s", listEnv.Data)
	}
	var items []map[string]any
	if err := json.Unmarshal(listEnv.Data, &items); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	found := false
	for _, it := range items {
		if it["id"] == subID {
			found = true
			if it["secrets_state"] != "present" {
				t.Errorf("list item secrets_state = %v, want present", it["secrets_state"])
			}
		}
	}
	if !found {
		t.Fatalf("submission %s not found in list response", subID)
	}
}

func TestCreateSubmission_NoSecrets_StateNone(t *testing.T) {
	srv := testServer()
	_, subID := createTestSubmission(t, srv)

	env := doGet(t, srv, "/api/v1/submissions/"+subID)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if s, _ := data["secrets_state"].(string); s != "none" && s != "" {
		t.Errorf("secrets_state = %v, want none/absent for a submission with no secrets", data["secrets_state"])
	}
}

func TestCreateSubmission_InvalidSecretName(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"not-a-valid-name": "value"},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Fatalf("error = %+v, want VALIDATION_ERROR", env.Error)
	}
	if !strings.Contains(env.Error.Message, "not-a-valid-name") {
		t.Errorf("error message %q does not name the offending secret", env.Error.Message)
	}
}

func TestCreateSubmission_InvalidSecretsRetention(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id":       wfID,
		"inputs":            map[string]any{"reads_r1": "test.fastq"},
		"secrets_retention": "not-a-policy",
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Fatalf("error = %+v, want VALIDATION_ERROR", env.Error)
	}
}

func TestCreateSubmission_SecretsRetentionDefaultsToServerPolicy(t *testing.T) {
	policy, _ := model.ParseSecretsRetention("ttl:1h")
	srv := testServer(WithSecretsRetention(policy))
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if data["secrets_retention"] != "ttl:1h0m0s" {
		t.Errorf("secrets_retention = %v, want ttl:1h0m0s (server default applied)", data["secrets_retention"])
	}

	// An explicit "keep" always wins over the server default.
	bodyJSON2, _ := json.Marshal(map[string]any{
		"workflow_id":       wfID,
		"inputs":            map[string]any{"reads_r1": "test.fastq"},
		"secrets":           map[string]string{"A": "valuevalue"},
		"secrets_retention": "keep",
	})
	w2, env2 := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON2), secretsTestToken)
	if w2.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w2.Code, w2.Body.String())
	}
	var data2 map[string]any
	json.Unmarshal(env2.Data, &data2)
	if data2["secrets_retention"] != "keep" {
		t.Errorf("secrets_retention = %v, want keep (explicit override)", data2["secrets_retention"])
	}
}

// registerSecretInputWorkflow directly persists a workflow with a
// cwltool:Secrets-declared input, bypassing the CWL parser (its own tests
// cover extractSecretInputs) — this test is about the server's stripping
// behavior at submission time.
func registerSecretInputWorkflow(t *testing.T, srv *Server) string {
	t.Helper()
	wf := &model.Workflow{
		ID:           "wf_secret_input_test",
		Name:         "wf-secret-input-test",
		Class:        "Workflow",
		CWLVersion:   "v1.2",
		RawCWL:       "{}",
		Inputs:       []model.WorkflowInput{{ID: "password", Type: "string"}, {ID: "reads_r1", Type: "File"}},
		SecretInputs: []string{"password"},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := srv.store.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("create secret-input workflow: %v", err)
	}
	return wf.ID
}

func TestCreateSubmission_CwltoolSecretsStripped(t *testing.T) {
	srv := testServer()
	wfID := registerSecretInputWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"password": "hunter2",
			"reads_r1": "test.fastq",
		},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	if bodyContainsValue(w.Body.Bytes(), "hunter2") {
		t.Fatalf("create response leaks the stripped secret value: %s", w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID, _ := data["id"].(string)

	inputs, _ := data["inputs"].(map[string]any)
	if inputs["password"] != model.SecretInputPlaceholder {
		t.Errorf("inputs[password] = %v, want placeholder %q", inputs["password"], model.SecretInputPlaceholder)
	}

	// submitted_inputs is only populated in a subsequent GET (the create
	// response's in-memory sub.SubmittedInputs is never set by the handler —
	// see the "submitted_inputs is an immutable snapshot" comment on
	// model.Submission; store.CreateSubmission computes the DB column from
	// sub.Inputs internally). Fetch it back to verify the placeholder
	// survived the snapshot, same as TestGetSubmission_DetailReturnsSubmittedInputsUnchangedAfterRewrite.
	getW, getEnv := doGetAs(t, srv, "/api/v1/submissions/"+subID, secretsTestToken)
	if getW.Code != http.StatusOK {
		t.Fatalf("get submission: status=%d, body=%s", getW.Code, getW.Body.String())
	}
	var getData map[string]any
	json.Unmarshal(getEnv.Data, &getData)
	submitted, _ := getData["submitted_inputs"].(map[string]any)
	if submitted["password"] != model.SecretInputPlaceholder {
		t.Errorf("submitted_inputs[password] = %v, want placeholder %q", submitted["password"], model.SecretInputPlaceholder)
	}

	names, _ := data["secret_names"].([]any)
	wantName := model.SecretNameForInput("password")
	foundName := false
	for _, n := range names {
		if n == wantName {
			foundName = true
		}
	}
	if !foundName {
		t.Errorf("secret_names = %v, want to contain %q", names, wantName)
	}

	// Confirm the value is actually retrievable server-side (round-tripped
	// through the store, not just dropped).
	sub, err := srv.store.GetSubmission(context.Background(), subID)
	if err != nil || sub == nil {
		t.Fatalf("get submission: %v", err)
	}
	if sub.Secrets[wantName] != "hunter2" {
		t.Errorf("stored secret %s = %q, want hunter2", wantName, sub.Secrets[wantName])
	}
}

// cwltoolSecretsCWL is a minimal single-file CWL v1.2 Workflow declaring a
// top-level cwltool:Secrets hint (shorthand key, no $namespaces block —
// see internal/parser.TestToModel_CwltoolSecrets_Shorthand for the same
// shape at the parser-unit level). "password" is wired into the step (as
// an unused-by-argv tool input) so the validator doesn't flag it as a
// dangling top-level input.
const cwltoolSecretsCWL = `#!/usr/bin/env cwl-runner
cwlVersion: v1.2
class: Workflow

hints:
  cwltool:Secrets:
    secrets: [password]

inputs:
  password:
    type: string
  message:
    type: string

steps:
  echo:
    run:
      class: CommandLineTool
      baseCommand: echo
      inputs:
        msg:
          type: string
          inputBinding:
            position: 1
        pw:
          type: string
      outputs:
        out:
          type: stdout
      stdout: output.txt
    in:
      msg: message
      pw: password
    out: [out]

outputs:
  result:
    type: File
    outputSource: echo/out
`

// TestCreateSubmission_CwltoolSecretsStripped_ViaCWLRegistration is the full
// end-to-end path for cwltool:Secrets stripping: real CWL text with an
// actual `cwltool:Secrets` hint, parsed and persisted through
// POST /api/v1/workflows/ (internal/parser.ParseGraph + ToModel +
// store.CreateWorkflow — the exact path that used to silently drop
// SecretInputs because workflows.secret_inputs was never wired), then a
// submission created by referencing that workflow's id (store.GetWorkflow
// round trip), asserting the stripping happens exactly as in the
// in-memory-workflow variant above.
func TestCreateSubmission_CwltoolSecretsStripped_ViaCWLRegistration(t *testing.T) {
	srv := testServer()

	wfBody, _ := json.Marshal(map[string]any{"cwl": cwltoolSecretsCWL})
	wfW, wfEnv := doPostAs(t, srv, "/api/v1/workflows/", string(wfBody), secretsTestToken)
	if wfW.Code != http.StatusCreated {
		t.Fatalf("register workflow: status=%d, body=%s", wfW.Code, wfW.Body.String())
	}
	var wfData map[string]any
	json.Unmarshal(wfEnv.Data, &wfData)
	wfID, _ := wfData["id"].(string)
	if wfID == "" {
		t.Fatalf("registered workflow missing id: %v", wfData)
	}
	secretInputs, _ := wfData["secret_inputs"].([]any)
	if len(secretInputs) != 1 || secretInputs[0] != "password" {
		t.Fatalf("registered workflow secret_inputs = %v, want [password] (parser -> store round trip)", wfData["secret_inputs"])
	}

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"password": "hunter2-e2e",
			"message":  "hello",
		},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("create submission: status=%d, body=%s", w.Code, w.Body.String())
	}
	if bodyContainsValue(w.Body.Bytes(), "hunter2-e2e") {
		t.Fatalf("create response leaks the stripped secret value: %s", w.Body.String())
	}

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID, _ := data["id"].(string)

	inputs, _ := data["inputs"].(map[string]any)
	if inputs["password"] != model.SecretInputPlaceholder {
		t.Errorf("inputs[password] = %v, want placeholder %q", inputs["password"], model.SecretInputPlaceholder)
	}
	if data["secrets_state"] != "present" {
		t.Errorf("secrets_state = %v, want present", data["secrets_state"])
	}

	sub, err := srv.store.GetSubmission(context.Background(), subID)
	if err != nil || sub == nil {
		t.Fatalf("get submission: %v", err)
	}
	wantName := model.SecretNameForInput("password")
	if sub.Secrets[wantName] != "hunter2-e2e" {
		t.Errorf("stored secret %s = %q, want hunter2-e2e", wantName, sub.Secrets[wantName])
	}
}

func TestCreateSubmission_CwltoolSecretsNonStringRejected(t *testing.T) {
	srv := testServer()
	wfID := registerSecretInputWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"password": 12345,
			"reads_r1": "test.fastq",
		},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Fatalf("error = %+v, want VALIDATION_ERROR", env.Error)
	}
}

// registerCollidingSecretInputsWorkflow declares two cwltool:Secrets inputs
// ("pw-1" and "pw_1") whose derived names collide: SecretNameForInput
// collapses every character outside [A-Z0-9] to '_', so both map to
// "INPUT_PW_1".
func registerCollidingSecretInputsWorkflow(t *testing.T, srv *Server) string {
	t.Helper()
	wf := &model.Workflow{
		ID:         "wf_secret_collision_test",
		Name:       "wf-secret-collision-test",
		Class:      "Workflow",
		CWLVersion: "v1.2",
		RawCWL:     "{}",
		Inputs: []model.WorkflowInput{
			{ID: "pw-1", Type: "string"},
			{ID: "pw_1", Type: "string"},
			{ID: "reads_r1", Type: "File"},
		},
		SecretInputs: []string{"pw-1", "pw_1"},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := srv.store.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("create colliding-secret-inputs workflow: %v", err)
	}
	return wf.ID
}

// TestCreateSubmission_CwltoolSecretsCollidingInputsRejected is the #260 M12
// regression test: two declared-secret inputs that derive the same name
// must be rejected with 400 instead of silently overwriting one another in
// the submission's secrets map.
func TestCreateSubmission_CwltoolSecretsCollidingInputsRejected(t *testing.T) {
	srv := testServer()
	wfID := registerCollidingSecretInputsWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"pw-1":     "value-one",
			"pw_1":     "value-two",
			"reads_r1": "test.fastq",
		},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Fatalf("error = %+v, want VALIDATION_ERROR", env.Error)
	}
	if !strings.Contains(env.Error.Message, "pw-1") || !strings.Contains(env.Error.Message, "pw_1") {
		t.Errorf("error message %q does not name both colliding inputs", env.Error.Message)
	}
	if bodyContainsValue(w.Body.Bytes(), "value-one") || bodyContainsValue(w.Body.Bytes(), "value-two") {
		t.Errorf("rejected create response leaks a secret value: %s", w.Body.String())
	}
}

// TestCreateSubmission_CwltoolSecretsCollidesWithSuppliedSecret is the #260
// M12 regression test for the other collision direction: a declared-secret
// input whose derived name matches a key the caller also supplied directly
// in "secrets" must be rejected with 400 instead of one silently clobbering
// the other.
func TestCreateSubmission_CwltoolSecretsCollidesWithSuppliedSecret(t *testing.T) {
	srv := testServer()
	wfID := registerSecretInputWorkflow(t, srv) // declares "password" -> derives INPUT_PASSWORD

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs": map[string]any{
			"password": "hunter2",
			"reads_r1": "test.fastq",
		},
		"secrets": map[string]string{"INPUT_PASSWORD": "supplied-value"},
	})
	w, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrValidation {
		t.Fatalf("error = %+v, want VALIDATION_ERROR", env.Error)
	}
	if !strings.Contains(env.Error.Message, "password") || !strings.Contains(env.Error.Message, "INPUT_PASSWORD") {
		t.Errorf("error message %q does not name the offending input and derived name", env.Error.Message)
	}
	if bodyContainsValue(w.Body.Bytes(), "hunter2") || bodyContainsValue(w.Body.Bytes(), "supplied-value") {
		t.Errorf("rejected create response leaks a secret value: %s", w.Body.String())
	}
}

func TestCreateSubmission_CwltoolSecretsOptionalInputAbsent(t *testing.T) {
	srv := testServer()
	wfID := registerSecretInputWorkflow(t, srv)

	// "password" declared secret but not supplied: must not error.
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
	})
	w, _ := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}
}

// TestCreateSubmission_AnonymousSecretsRefused relies on doPost's default
// behavior (no Authorization header) resolving to the anonymous user under
// testServer()'s default AnonymousConfig — see server_test.go.
func TestCreateSubmission_AnonymousSecretsRefused(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrForbidden {
		t.Fatalf("error = %+v, want FORBIDDEN", env.Error)
	}
}

// TestCreateSubmission_AnonymousCwltoolSecretsRefused covers the
// cwltool:Secrets-declared-input path (as opposed to explicit "secrets"),
// which must be refused for the same reason.
func TestCreateSubmission_AnonymousCwltoolSecretsRefused(t *testing.T) {
	srv := testServer()
	wfID := registerSecretInputWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"password": "hunter2", "reads_r1": "test.fastq"},
	})
	w, env := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != model.ErrForbidden {
		t.Fatalf("error = %+v, want FORBIDDEN", env.Error)
	}
}

func TestRetrySubmission_PurgedSecretsRefused(t *testing.T) {
	srv, st := testServerWithStore()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID := data["id"].(string)

	// Force the submission FAILED (retry requires FAILED) and purge secrets.
	sub, err := st.GetSubmission(context.Background(), subID)
	if err != nil || sub == nil {
		t.Fatalf("get submission: %v", err)
	}
	sub.State = model.SubmissionStateFailed
	now := time.Now().UTC()
	sub.CompletedAt = &now
	if err := st.UpdateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("update submission: %v", err)
	}
	if err := st.PurgeSubmissionSecrets(context.Background(), subID, now); err != nil {
		t.Fatalf("purge: %v", err)
	}

	w, env2 := doPutAs(t, srv, "/api/v1/submissions/"+subID+"/retry", "", secretsTestToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("retry after purge: status=%d, want 409, body=%s", w.Code, w.Body.String())
	}
	if env2.Error == nil || !strings.Contains(env2.Error.Message, "purged") {
		t.Errorf("error = %+v, want a message mentioning purged secrets", env2.Error)
	}
}

func TestDeleteSubmissionSecrets_PurgesAndReturns404WhenNone(t *testing.T) {
	srv, st := testServerWithStore()

	// No secrets at all: 404.
	wfID, subID := createTestSubmission(t, srv)
	w := doDelete(t, srv, "/api/v1/submissions/"+subID+"/secrets")
	if w.Code != http.StatusNotFound {
		t.Fatalf("delete secrets on a submission with none: status=%d, want 404, body=%s", w.Code, w.Body.String())
	}

	// With secrets: 200, purged, and a second delete now also 404s. Reuses
	// wfID from above — registering the same packed CWL content twice
	// dedupes to the existing workflow row (200, not 201).
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	secretSubID := data["id"].(string)

	// DELETE requires a terminal submission (H3): force it there directly
	// via the store, as the scheduler would once the submission finishes.
	forceSubmissionTerminal(t, st, secretSubID, model.SubmissionStateCompleted)

	w2 := doDeleteAs(t, srv, "/api/v1/submissions/"+secretSubID+"/secrets", secretsTestToken)
	if w2.Code != http.StatusOK {
		t.Fatalf("delete secrets: status=%d, want 200, body=%s", w2.Code, w2.Body.String())
	}
	if bodyContainsValue(w2.Body.Bytes(), `"valuevalue"`) {
		t.Errorf("delete secrets response may leak a value: %s", w2.Body.String())
	}

	getW, getEnv := doGetAs(t, srv, "/api/v1/submissions/"+secretSubID, secretsTestToken)
	if getW.Code != http.StatusOK {
		t.Fatalf("get submission after purge: status=%d, body=%s", getW.Code, getW.Body.String())
	}
	var getData map[string]any
	json.Unmarshal(getEnv.Data, &getData)
	if getData["secrets_state"] != "purged" {
		t.Errorf("secrets_state after purge = %v, want purged", getData["secrets_state"])
	}

	w3 := doDeleteAs(t, srv, "/api/v1/submissions/"+secretSubID+"/secrets", secretsTestToken)
	if w3.Code != http.StatusNotFound {
		t.Fatalf("second delete: status=%d, want 404, body=%s", w3.Code, w3.Body.String())
	}
}

func TestDeleteSubmissionSecrets_CascadesToChildSubmissions(t *testing.T) {
	srv, st := testServerWithStore()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	parentID := data["id"].(string)

	// Seed a subworkflow proxy task + child submission carrying its own
	// secret (as the scheduler's subworkflow dispatch would).
	ctx := context.Background()
	proxy := &model.Task{
		ID:           "task_proxy_secrets_cascade",
		SubmissionID: parentID,
		StepID:       "work",
		State:        model.TaskStateRunning,
		ExecutorType: model.ExecutorTypeSubworkflow,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
	}
	if err := st.CreateTask(ctx, proxy); err != nil {
		t.Fatalf("seed proxy task: %v", err)
	}
	child := &model.Submission{
		ID:               "sub_child_secrets_cascade",
		WorkflowID:       wfID,
		State:            model.SubmissionStateRunning,
		Inputs:           map[string]any{},
		SubmittedBy:      "secrets-tester",
		ParentTaskID:     proxy.ID,
		CreatedAt:        time.Now().UTC(),
		Secrets:          map[string]string{"B": "child-secret"},
		SecretNames:      []string{"B"},
		SecretsRetention: "keep",
	}
	if err := st.CreateSubmission(ctx, child); err != nil {
		t.Fatalf("seed child submission: %v", err)
	}

	// The top-level DELETE requires the parent to be terminal (H3); the
	// cascade purges children unconditionally regardless of their own state
	// (see purgeSecretsCascade), so the still-RUNNING child above is left as is.
	forceSubmissionTerminal(t, st, parentID, model.SubmissionStateCompleted)

	w := doDeleteAs(t, srv, "/api/v1/submissions/"+parentID+"/secrets", secretsTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("delete secrets: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	gotChild, err := st.GetSubmission(ctx, child.ID)
	if err != nil || gotChild == nil {
		t.Fatalf("get child submission: %v", err)
	}
	if gotChild.SecretsState() != "purged" {
		t.Errorf("child SecretsState() = %q, want purged (cascade)", gotChild.SecretsState())
	}
}

// TestDeleteSubmissionSecrets_NonTerminalRefused verifies the #260 H3 state
// guard: purging a submission that is still non-terminal is refused with
// 409 rather than deleting secrets a running task may still depend on.
func TestDeleteSubmissionSecrets_NonTerminalRefused(t *testing.T) {
	srv := testServer()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID := data["id"].(string)

	// A freshly-created submission is PENDING (non-terminal): DELETE must
	// refuse it with 409, and the secrets must remain present.
	w := doDeleteAs(t, srv, "/api/v1/submissions/"+subID+"/secrets", secretsTestToken)
	if w.Code != http.StatusConflict {
		t.Fatalf("delete secrets on non-terminal submission: status=%d, want 409, body=%s", w.Code, w.Body.String())
	}

	getW, getEnv := doGetAs(t, srv, "/api/v1/submissions/"+subID, secretsTestToken)
	if getW.Code != http.StatusOK {
		t.Fatalf("get submission: status=%d, body=%s", getW.Code, getW.Body.String())
	}
	var getData map[string]any
	json.Unmarshal(getEnv.Data, &getData)
	if getData["secrets_state"] != "present" {
		t.Errorf("secrets_state after refused purge = %v, want present", getData["secrets_state"])
	}
}

// TestDeleteSubmissionSecrets_ScrubsTaskRows verifies the #260 H3 fix:
// purging a submission's secrets must also clear the per-task secret copy
// embedded in tasks.runtime_hints (PurgeSubmissionSecrets only clears the
// submissions row), for both the submission itself and every cascaded
// child.
func TestDeleteSubmissionSecrets_ScrubsTaskRows(t *testing.T) {
	srv, st := testServerWithStore()
	wfID := createTestWorkflow(t, srv)

	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
		"secrets":     map[string]string{"A": "valuevalue"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	subID := data["id"].(string)

	ctx := context.Background()
	task := &model.Task{
		ID:           "task_purge_scrub",
		SubmissionID: subID,
		StepID:       "work",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"A": "valuevalue"},
			SecretEnvNames: []string{"A"},
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: "bearer-value"},
			},
		},
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	forceSubmissionTerminal(t, st, subID, model.SubmissionStateCompleted)

	w := doDeleteAs(t, srv, "/api/v1/submissions/"+subID+"/secrets", secretsTestToken)
	if w.Code != http.StatusOK {
		t.Fatalf("delete secrets: status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	got, err := st.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("get task: %v", err)
	}
	if len(got.RuntimeHints.Secrets) != 0 {
		t.Errorf("task Secrets after purge = %v, want empty", got.RuntimeHints.Secrets)
	}
	if got.RuntimeHints.StagerOverrides.HTTPCredential != nil {
		t.Errorf("task HTTPCredential after purge = %v, want nil", got.RuntimeHints.StagerOverrides.HTTPCredential)
	}
	// Metadata must survive.
	if len(got.RuntimeHints.SecretEnvNames) != 1 || got.RuntimeHints.SecretEnvNames[0] != "A" {
		t.Errorf("task SecretEnvNames after purge = %v, want [A]", got.RuntimeHints.SecretEnvNames)
	}
}
