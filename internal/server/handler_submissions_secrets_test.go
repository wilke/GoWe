package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		"secrets":     map[string]string{"A": "v"},
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
		"secrets":           map[string]string{"A": "v"},
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
		"secrets":     map[string]string{"A": "v"},
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
		"secrets":     map[string]string{"A": "v"},
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
	srv := testServer()

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
		"secrets":     map[string]string{"A": "v"},
	})
	_, env := doPostAs(t, srv, "/api/v1/submissions/", string(bodyJSON), secretsTestToken)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	secretSubID := data["id"].(string)

	w2 := doDeleteAs(t, srv, "/api/v1/submissions/"+secretSubID+"/secrets", secretsTestToken)
	if w2.Code != http.StatusOK {
		t.Fatalf("delete secrets: status=%d, want 200, body=%s", w2.Code, w2.Body.String())
	}
	if bodyContainsValue(w2.Body.Bytes(), `"v"`) {
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
		"secrets":     map[string]string{"A": "v"},
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
