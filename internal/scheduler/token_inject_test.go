package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

// TestDelegatedTokenInjectionRoundTrip verifies the end-to-end delegation path:
// a submitter token is encrypted at rest, decrypted transparently on read, and
// then injected as a bearer credential into a task's runtime hints (which the
// worker later exposes as BVBRC_TOKEN). This is the security-critical path the
// encryption work must not break.
func TestDelegatedTokenInjectionRoundTrip(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	cipher, err := tokencrypt.New(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	st.ConfigureTokenEncryption(cipher, true)

	const token = "bvbrc|un=alice|expiry=9999999999|sig=abc123"
	sub := &model.Submission{
		ID:           "sub_deleg",
		WorkflowID:   "wf_1",
		WorkflowName: "wf",
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		UserToken:    token,
		AuthProvider: "bvbrc",
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	// Reload from the store — this is how the scheduler obtains the submission,
	// with the token decrypted back to plaintext in memory.
	loaded, err := st.GetSubmission(ctx, "sub_deleg")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if loaded.UserToken != token {
		t.Fatalf("token not decrypted on read: got %q", loaded.UserToken)
	}

	// Inject via the scheduler's delegation path (wsStager nil is fine).
	l := &Loop{}
	task := &model.Task{
		ID:           "task_deleg",
		ExecutorType: model.ExecutorTypeBVBRC,
	}
	l.addUserToken(task, loaded)

	if task.RuntimeHints == nil ||
		task.RuntimeHints.StagerOverrides == nil ||
		task.RuntimeHints.StagerOverrides.HTTPCredential == nil {
		t.Fatal("expected injected HTTP credential in runtime hints")
	}
	cred := task.RuntimeHints.StagerOverrides.HTTPCredential
	if cred.Type != "bearer" || cred.Token != token {
		t.Fatalf("injected credential mismatch: type=%q token=%q", cred.Type, cred.Token)
	}

	// Persisting the task must not fail (and, per store tests, encrypts the
	// embedded token at rest). The in-memory task keeps the plaintext token.
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.RuntimeHints.StagerOverrides.HTTPCredential.Token != token {
		t.Fatalf("CreateTask mutated in-memory token: %q", task.RuntimeHints.StagerOverrides.HTTPCredential.Token)
	}
}
