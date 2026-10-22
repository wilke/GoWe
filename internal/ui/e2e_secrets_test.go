package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// TestSubmissionDetailPage_SecretsNeverEchoed is the #260 acceptance
// battery's group-1 "UI submission page HTML" non-echo check: the web UI's
// submission detail page (GET /submissions/{id}) must show only secrets
// metadata (names, state, retention, purged_at — see
// internal/ui/templates.go's "Secrets" panel), never a value.
//
// Seeds the submission directly through the store (mirroring
// internal/server/handler_submissions.go's own secrets handling: the real
// value lives only in Submission.Secrets, json:"-" and never echoed) rather
// than driving real execution — this test is purely about template
// rendering, not delivery, so it deliberately doesn't need a worker.
func TestSubmissionDetailPage_SecretsNeverEchoed(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})

	r := chi.NewRouter()
	u.RegisterRoutes(r)

	_, cookie := newTestSession(t, u, "secrets-ui-tester", string(model.RoleUser))

	const secretValue = "s3cr3t-VALUE-9f2a-ui"
	now := time.Now().UTC()
	wf := &model.Workflow{
		ID:         "wf_ui_secrets",
		Name:       "ui-secrets-test",
		CWLVersion: "v1.2",
		RawCWL:     "test",
		CreatedBy:  "secrets-ui-tester",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	sub := &model.Submission{
		ID:               "sub_ui_secrets",
		WorkflowID:       wf.ID,
		WorkflowName:     wf.Name,
		State:            model.SubmissionStateCompleted,
		SubmittedBy:      "secrets-ui-tester",
		Inputs:           map[string]any{"pw": model.SecretInputPlaceholder},
		Outputs:          map[string]any{},
		Labels:           map[string]string{},
		CreatedAt:        now,
		CompletedAt:      &now,
		Secrets:          map[string]string{"INPUT_PW": secretValue},
		SecretNames:      []string{"INPUT_PW"},
		SecretsRetention: "keep",
	}
	if err := st.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+sub.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /submissions/%s: status = %d, want 200, body: %s", sub.ID, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secretValue) {
		t.Fatalf("submission detail page leaks the secret value:\n%s", body)
	}
	// Metadata must still render: this proves the panel actually rendered
	// (not just that the HTML is empty/erroring), so the non-echo assertion
	// above is meaningful.
	if !strings.Contains(body, "INPUT_PW") {
		t.Errorf("submission detail page = missing secret name INPUT_PW in metadata panel:\n%s", body)
	}
	if !strings.Contains(body, "present") {
		t.Errorf("submission detail page = missing secrets_state (present) in metadata panel:\n%s", body)
	}
}
