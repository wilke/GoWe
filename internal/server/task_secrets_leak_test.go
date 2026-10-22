package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// #260 C1/H2 regression coverage: every user-facing task surface must never
// echo RuntimeHints.Secrets or StagerOverrides.HTTPCredential, and a worker
// task-completion report must scrub both before persisting.

const leakTestSecretValue = "leak-check-secret-value-123"
const leakTestBearerValue = "bearer-leak-check-token-456"

// seedTaskWithSecret creates a task on subID carrying a submission-time
// secret and an HTTP credential override in RuntimeHints (as addSecrets /
// the scheduler would before dispatch), directly via the store.
func seedTaskWithSecret(t *testing.T, st store.Store, subID, taskID string) *model.Task {
	t.Helper()
	task := &model.Task{
		ID:           taskID,
		SubmissionID: subID,
		StepID:       "work",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		ExternalID:   taskID,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Job:          map[string]any{},
		ScatterIndex: -1,
		RuntimeHints: &model.RuntimeHints{
			Secrets:        map[string]string{"HF_TOKEN": leakTestSecretValue},
			SecretEnvNames: []string{"HF_TOKEN"},
			SecretInputs:   []string{"pw"},
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: leakTestBearerValue},
			},
		},
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task with secret: %v", err)
	}
	return task
}

func assertNoSecretLeak(t *testing.T, body []byte, context string) {
	t.Helper()
	if strings.Contains(string(body), leakTestSecretValue) {
		t.Errorf("%s: response leaks secret value: %s", context, body)
	}
	if strings.Contains(string(body), leakTestBearerValue) {
		t.Errorf("%s: response leaks HTTP credential token: %s", context, body)
	}
}

// assertRuntimeHintsMetadataSurvives checks that a sanitized runtime_hints
// object still carries secret_env_names/secret_inputs (metadata, never a
// value) after scrubbing.
func assertRuntimeHintsMetadataSurvives(t *testing.T, rh map[string]any) {
	t.Helper()
	if rh == nil {
		t.Fatal("runtime_hints missing from response")
	}
	if _, has := rh["secrets"]; has {
		t.Errorf("runtime_hints.secrets present in response: %v", rh["secrets"])
	}
	names, _ := rh["secret_env_names"].([]any)
	if len(names) != 1 || names[0] != "HF_TOKEN" {
		t.Errorf("secret_env_names = %v, want [HF_TOKEN]", rh["secret_env_names"])
	}
	inputs, _ := rh["secret_inputs"].([]any)
	if len(inputs) != 1 || inputs[0] != "pw" {
		t.Errorf("secret_inputs = %v, want [pw]", rh["secret_inputs"])
	}
}

func TestGetTask_DoesNotLeakSecrets(t *testing.T) {
	srv, st := testServerWithStore()
	_, subID := createTestSubmission(t, srv)
	task := seedTaskWithSecret(t, st, subID, "task_leak_get")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/submissions/"+subID+"/tasks/"+task.ID, nil)
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET task: status=%d, body=%s", w.Code, w.Body.String())
	}
	assertNoSecretLeak(t, w.Body.Bytes(), "GET task")

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	rh, _ := data["runtime_hints"].(map[string]any)
	assertRuntimeHintsMetadataSurvives(t, rh)

	// The store's own copy must be untouched (sanitizeTaskCredentials must
	// copy, never mutate).
	stored, err := st.GetTask(context.Background(), task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get stored task: %v", err)
	}
	if stored.RuntimeHints.Secrets["HF_TOKEN"] != leakTestSecretValue {
		t.Errorf("stored task secret was mutated by sanitization: %v", stored.RuntimeHints.Secrets)
	}
	if stored.RuntimeHints.StagerOverrides.HTTPCredential == nil {
		t.Errorf("stored task HTTPCredential was mutated by sanitization")
	}
}

func TestListTasks_DoesNotLeakSecrets(t *testing.T) {
	srv, st := testServerWithStore()
	_, subID := createTestSubmission(t, srv)
	seedTaskWithSecret(t, st, subID, "task_leak_list")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/submissions/"+subID+"/tasks/", nil)
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list tasks: status=%d, body=%s", w.Code, w.Body.String())
	}
	assertNoSecretLeak(t, w.Body.Bytes(), "list tasks")

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	var items []map[string]any
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d tasks, want 1", len(items))
	}
	rh, _ := items[0]["runtime_hints"].(map[string]any)
	assertRuntimeHintsMetadataSurvives(t, rh)
}

func TestGetSubmission_EmbeddedTasksDoNotLeakSecrets(t *testing.T) {
	srv, st := testServerWithStore()
	_, subID := createTestSubmission(t, srv)
	seedTaskWithSecret(t, st, subID, "task_leak_embedded")

	env := doGet(t, srv, "/api/v1/submissions/"+subID)
	assertNoSecretLeak(t, env.Data, "GET submission (embedded tasks)")

	var data map[string]any
	json.Unmarshal(env.Data, &data)
	tasks, _ := data["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("got %d embedded tasks, want 1", len(tasks))
	}
	taskMap, _ := tasks[0].(map[string]any)
	rh, _ := taskMap["runtime_hints"].(map[string]any)
	assertRuntimeHintsMetadataSurvives(t, rh)
}

// adminUserToken creates an admin-role user directly in the store and
// returns a matching bearer token, so admin-only routes can be exercised
// through the real apiAuthMiddleware (GetOrCreateUser finds the pre-seeded
// row and preserves its Role).
func adminUserToken(t *testing.T, st store.Store, username string) string {
	t.Helper()
	ctx := context.Background()
	user, err := st.GetOrCreateUser(ctx, username, model.ProviderBVBRC)
	if err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	user.Role = model.RoleAdmin
	if err := st.UpdateUser(ctx, user); err != nil {
		t.Fatalf("promote admin user: %v", err)
	}
	return "un=" + username + "|tokenid=t-" + username + "|expiry=4102444800|sig=s"
}

func TestListActiveTasks_DoesNotLeakSecrets(t *testing.T) {
	srv, st := testServerWithStore()
	_, subID := createTestSubmission(t, srv)
	task := seedTaskWithSecret(t, st, subID, "task_leak_active")
	// GetActiveTasks selects QUEUED/RUNNING tasks; seedTaskWithSecret already
	// creates it QUEUED.
	_ = task

	adminToken := adminUserToken(t, st, "leak-test-admin")

	req := httptest.NewRequest("GET", "/api/v1/admin/tasks/active", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list active tasks: status=%d, body=%s", w.Code, w.Body.String())
	}
	assertNoSecretLeak(t, w.Body.Bytes(), "admin active tasks")

	var env envelope
	json.Unmarshal(w.Body.Bytes(), &env)
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	tasks, _ := data["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("got %d active tasks, want 1", len(tasks))
	}
	taskMap, _ := tasks[0].(map[string]any)
	rh, _ := taskMap["runtime_hints"].(map[string]any)
	assertRuntimeHintsMetadataSurvives(t, rh)
}

// cancelOnFlushRecorder cancels its own request context the first time the
// handler flushes — i.e. right after handleSSESubmission writes and flushes
// the "init" event, before it would otherwise block in its poll loop. This
// makes an SSE test terminate deterministically and synchronously, with no
// sleep and no reliance on the poll ticker interval.
type cancelOnFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel  func()
	flushed bool
}

func (r *cancelOnFlushRecorder) Flush() {
	r.ResponseRecorder.Flush()
	if !r.flushed {
		r.flushed = true
		r.cancel()
	}
}

// TestSSESubmission_InitEventDoesNotLeakSecrets covers the SSE "init" event
// (handler_sse.go), which serializes the same *model.Submission (with
// embedded Tasks) as GET /submissions/{id}. It calls handleSSESubmission
// directly (with a chi route context standing in for the router) rather
// than through srv.ServeHTTP: the global loggingMiddleware wraps the
// ResponseWriter in a *statusWriter that only embeds the http.ResponseWriter
// interface, so it never satisfies http.Flusher regardless of what the real
// underlying writer supports — a pre-existing, separate issue outside #260's
// scope that would make any full-stack SSE test fail with "SSE not
// supported" no matter what it asserts.
func TestSSESubmission_InitEventDoesNotLeakSecrets(t *testing.T) {
	srv, st := testServerWithStore()
	_, subID := createTestSubmission(t, srv)
	seedTaskWithSecret(t, st, subID, "task_leak_sse")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", subID)

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), chi.RouteCtxKey, rctx))
	defer cancel()
	req := httptest.NewRequest("GET", "/api/v1/sse/submissions/"+subID, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	w := &cancelOnFlushRecorder{ResponseRecorder: rec, cancel: cancel}
	srv.handleSSESubmission(w, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: init") {
		t.Fatalf("expected an SSE init event, got: %s", body)
	}
	assertNoSecretLeak(t, rec.Body.Bytes(), "SSE init event")
	if strings.Contains(body, `"secrets"`) {
		t.Errorf("SSE init event carries a runtime_hints.secrets field: %s", body)
	}
}

// TestWorkerTaskComplete_ScrubsSecretsBeforePersisting is the #260 H2
// regression test: a worker-reported terminal task must have its
// RuntimeHints.Secrets and StagerOverrides.HTTPCredential cleared before
// the row is persisted — previously only HTTPCredential was cleared, so
// every secret-bearing worker task kept a live copy at rest forever.
func TestWorkerTaskComplete_ScrubsSecretsBeforePersisting(t *testing.T) {
	srv, st := testServerWithStore()
	workerID := registerTestWorker(t, srv)
	_, subID := createTestSubmission(t, srv)
	task := seedTaskWithSecret(t, st, subID, "task_h2_scrub")

	checked := checkoutTask(t, srv, workerID)
	if checked.ID != task.ID {
		t.Fatalf("checked out %s, want %s", checked.ID, task.ID)
	}
	// The checkout payload is the one path allowed to carry the secret.
	if checked.RuntimeHints.Secrets["HF_TOKEN"] != leakTestSecretValue {
		t.Fatalf("checkout did not deliver the secret to the worker: %v", checked.RuntimeHints.Secrets)
	}

	body := `{"state":"SUCCESS","exit_code":0,"stdout":"ok","stderr":"","outputs":{}}`
	w, _ := doPut(t, srv, "/api/v1/workers/"+workerID+"/tasks/"+task.ID+"/complete", body)
	if w.Code != http.StatusOK {
		t.Fatalf("complete task: status=%d, body=%s", w.Code, w.Body.String())
	}

	stored, err := st.GetTask(context.Background(), task.ID)
	if err != nil || stored == nil {
		t.Fatalf("get stored task after complete: %v", err)
	}
	if len(stored.RuntimeHints.Secrets) != 0 {
		t.Errorf("stored task Secrets after complete = %v, want empty", stored.RuntimeHints.Secrets)
	}
	if stored.RuntimeHints.StagerOverrides != nil && stored.RuntimeHints.StagerOverrides.HTTPCredential != nil {
		t.Errorf("stored task HTTPCredential after complete = %v, want nil", stored.RuntimeHints.StagerOverrides.HTTPCredential)
	}
	// Metadata must survive.
	if len(stored.RuntimeHints.SecretEnvNames) != 1 || stored.RuntimeHints.SecretEnvNames[0] != "HF_TOKEN" {
		t.Errorf("stored task SecretEnvNames after complete = %v, want [HF_TOKEN]", stored.RuntimeHints.SecretEnvNames)
	}
}
