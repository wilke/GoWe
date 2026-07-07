package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

func testTokenCipher(t *testing.T) *tokencrypt.Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 7)
	}
	c, err := tokencrypt.New(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return c
}

func tokenSubmission(id, token string) *model.Submission {
	return &model.Submission{
		ID:           id,
		WorkflowID:   "wf_1",
		WorkflowName: "wf",
		State:        model.SubmissionStatePending,
		Inputs:       map[string]any{},
		UserToken:    token,
		AuthProvider: "bvbrc",
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
	}
}

func tokenTask(id, token string) *model.Task {
	return &model.Task{
		ID:           id,
		SubmissionID: "sub_1",
		StepID:       "step",
		State:        model.TaskStateQueued,
		ExecutorType: model.ExecutorTypeWorker,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		CreatedAt:    time.Now().UTC().Truncate(time.Second),
		RuntimeHints: &model.RuntimeHints{
			InjectBVBRCToken: true,
			StagerOverrides: &model.StagerOverrides{
				HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: token},
			},
		},
	}
}

func rawSubmissionToken(t *testing.T, st *SQLiteStore, id string) string {
	t.Helper()
	var tok string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT user_token FROM submissions WHERE id=?`, id).Scan(&tok); err != nil {
		t.Fatalf("raw query submission token: %v", err)
	}
	return tok
}

func rawTaskHints(t *testing.T, st *SQLiteStore, id string) string {
	t.Helper()
	var h string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT runtime_hints FROM tasks WHERE id=?`, id).Scan(&h); err != nil {
		t.Fatalf("raw query task hints: %v", err)
	}
	return h
}

func TestSubmissionTokenEncryptedAtRest(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	const secret = "bvbrc|un=me|SIG=deadbeef"
	if err := st.CreateSubmission(ctx, tokenSubmission("sub_enc", secret)); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	raw := rawSubmissionToken(t, st, "sub_enc")
	if !tokencrypt.IsEncrypted(raw) {
		t.Fatalf("stored token is not encrypted: %q", raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("stored token leaks plaintext: %q", raw)
	}

	got, err := st.GetSubmission(ctx, "sub_enc")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.UserToken != secret {
		t.Fatalf("decrypted token mismatch: got %q want %q", got.UserToken, secret)
	}

	// ListSubmissions must decrypt too.
	subs, _, err := st.ListSubmissions(ctx, model.ListOptions{})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(subs) != 1 || subs[0].UserToken != secret {
		t.Fatalf("list did not decrypt token: %+v", subs)
	}
}

func TestSubmissionTokenFailClosedWithoutKey(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(nil, true) // no cipher, refuse plaintext
	ctx := context.Background()

	if err := st.CreateSubmission(ctx, tokenSubmission("sub_fail", "a-token")); err == nil {
		t.Fatal("expected fail-closed error persisting token without a key, got nil")
	}
	// A submission with no token must still succeed.
	if err := st.CreateSubmission(ctx, tokenSubmission("sub_empty", "")); err != nil {
		t.Fatalf("empty-token submission should succeed: %v", err)
	}
}

func TestSubmissionTokenPlaintextAllowed(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(nil, false) // legacy plaintext mode
	ctx := context.Background()

	if err := st.CreateSubmission(ctx, tokenSubmission("sub_plain", "plain-token")); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	if raw := rawSubmissionToken(t, st, "sub_plain"); raw != "plain-token" {
		t.Fatalf("expected plaintext at rest, got %q", raw)
	}
}

func TestTaskRuntimeHintsTokenEncryptedAtRest(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	const secret = "worker-bearer-token"
	task := tokenTask("task_enc", secret)
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The caller's in-memory task must NOT be mutated to ciphertext.
	if task.RuntimeHints.StagerOverrides.HTTPCredential.Token != secret {
		t.Fatalf("CreateTask mutated in-memory token: %q", task.RuntimeHints.StagerOverrides.HTTPCredential.Token)
	}

	raw := rawTaskHints(t, st, "task_enc")
	if strings.Contains(raw, secret) {
		t.Fatalf("stored runtime_hints leaks plaintext token: %q", raw)
	}
	if !strings.Contains(raw, "enc:v1:") {
		t.Fatalf("stored runtime_hints token not encrypted: %q", raw)
	}

	got, err := st.GetTask(ctx, "task_enc")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.RuntimeHints.StagerOverrides.HTTPCredential.Token != secret {
		t.Fatalf("decrypted task token mismatch: got %q", got.RuntimeHints.StagerOverrides.HTTPCredential.Token)
	}
}

func TestReencryptPlaintextTokens(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Write rows in legacy plaintext mode.
	st.ConfigureTokenEncryption(nil, false)
	if err := st.CreateSubmission(ctx, tokenSubmission("sub_mig", "legacy-sub-token")); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	if err := st.CreateTask(ctx, tokenTask("task_mig", "legacy-task-token")); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Enable encryption and migrate.
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	nSub, nTask, err := st.ReencryptPlaintextTokens(ctx)
	if err != nil {
		t.Fatalf("reencrypt: %v", err)
	}
	if nSub != 1 || nTask != 1 {
		t.Fatalf("expected (1,1) rewritten, got (%d,%d)", nSub, nTask)
	}

	if raw := rawSubmissionToken(t, st, "sub_mig"); !tokencrypt.IsEncrypted(raw) {
		t.Fatalf("submission token not upgraded: %q", raw)
	}
	if raw := rawTaskHints(t, st, "task_mig"); strings.Contains(raw, "legacy-task-token") {
		t.Fatalf("task token not upgraded: %q", raw)
	}

	// Values still readable as plaintext.
	sub, err := st.GetSubmission(ctx, "sub_mig")
	if err != nil || sub.UserToken != "legacy-sub-token" {
		t.Fatalf("submission not readable post-migration: %v / %q", err, sub.UserToken)
	}
	task, err := st.GetTask(ctx, "task_mig")
	if err != nil || task.RuntimeHints.StagerOverrides.HTTPCredential.Token != "legacy-task-token" {
		t.Fatalf("task not readable post-migration: %v", err)
	}

	// Idempotent: a second pass rewrites nothing.
	nSub2, nTask2, err := st.ReencryptPlaintextTokens(ctx)
	if err != nil || nSub2 != 0 || nTask2 != 0 {
		t.Fatalf("second migration not idempotent: (%d,%d) err=%v", nSub2, nTask2, err)
	}
}
