package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

func secretsSubmission(id string, secrets map[string]string) *model.Submission {
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	return &model.Submission{
		ID:               id,
		WorkflowID:       "wf_1",
		WorkflowName:     "wf",
		State:            model.SubmissionStatePending,
		Inputs:           map[string]any{},
		Secrets:          secrets,
		SecretNames:      names,
		SecretsRetention: "keep",
		CreatedAt:        time.Now().UTC().Truncate(time.Second),
	}
}

func secretsTask(id string, secrets map[string]string) *model.Task {
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
			Secrets: secrets,
		},
	}
}

func rawSubmissionSecrets(t *testing.T, st *SQLiteStore, id string) *string {
	t.Helper()
	var v *string
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT secrets FROM submissions WHERE id=?`, id).Scan(&v); err != nil {
		t.Fatalf("raw query submission secrets: %v", err)
	}
	return v
}

func TestSubmissionSecretsRoundTrip(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	secrets := map[string]string{"HF_TOKEN": "hf_abc123", "API_KEY": "xyz"}
	sub := secretsSubmission("sub_secrets", secrets)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	got, err := st.GetSubmission(ctx, "sub_secrets")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if len(got.Secrets) != 2 || got.Secrets["HF_TOKEN"] != "hf_abc123" || got.Secrets["API_KEY"] != "xyz" {
		t.Fatalf("decrypted secrets mismatch: %+v", got.Secrets)
	}
	if len(got.SecretNames) != 2 {
		t.Fatalf("SecretNames = %v, want 2 entries", got.SecretNames)
	}
	if got.SecretsRetention != "keep" {
		t.Errorf("SecretsRetention = %q, want keep", got.SecretsRetention)
	}
	if got.SecretsPurgedAt != nil {
		t.Errorf("SecretsPurgedAt = %v, want nil", got.SecretsPurgedAt)
	}
	if got.SecretsState() != "present" {
		t.Errorf("SecretsState() = %q, want present", got.SecretsState())
	}

	// Secrets must never surface through list queries.
	subs, _, err := st.ListSubmissions(ctx, model.ListOptions{})
	if err != nil {
		t.Fatalf("list submissions: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 submission, got %d", len(subs))
	}
	if subs[0].Secrets != nil {
		t.Errorf("ListSubmissions leaked secrets: %+v", subs[0].Secrets)
	}
}

func TestSubmissionSecretsEncryptedAtRest(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	secrets := map[string]string{"HF_TOKEN": "super-secret-value"}
	if err := st.CreateSubmission(ctx, secretsSubmission("sub_enc", secrets)); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	raw := rawSubmissionSecrets(t, st, "sub_enc")
	if raw == nil || *raw == "" {
		t.Fatal("expected non-empty stored secrets")
	}
	if !tokencrypt.IsEncrypted(*raw) {
		t.Fatalf("stored secrets not encrypted: %q", *raw)
	}
	if strings.Contains(*raw, "super-secret-value") {
		t.Fatalf("stored secrets leak plaintext: %q", *raw)
	}
}

func TestSubmissionSecretsNilForNoSecrets(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	sub := secretsSubmission("sub_none", nil)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	if raw := rawSubmissionSecrets(t, st, "sub_none"); raw != nil {
		t.Errorf("expected NULL secrets column, got %v", raw)
	}

	got, err := st.GetSubmission(ctx, "sub_none")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.Secrets != nil {
		t.Errorf("Secrets = %+v, want nil", got.Secrets)
	}
	if got.SecretsState() != "none" {
		t.Errorf("SecretsState() = %q, want none", got.SecretsState())
	}
}

// TestSubmissionSecretsPreMigrationRowReadsAsNil simulates a row written
// before #260 (secrets/secret_names/secrets_purged_at all SQL NULL, matching
// what addColumnIfNotExists leaves on an upgraded pre-existing database) and
// confirms GetSubmission reads it back with a nil Secrets map and no error.
func TestSubmissionSecretsPreMigrationRowReadsAsNil(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	sub := secretsSubmission("sub_pre260", nil)
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	// Explicitly null out secrets_retention too, as a pre-migration row would
	// carry the column default rather than anything this package wrote.
	if _, err := st.db.ExecContext(ctx, `UPDATE submissions SET secrets=NULL, secret_names=NULL, secrets_retention='', secrets_purged_at=NULL WHERE id=?`, "sub_pre260"); err != nil {
		t.Fatalf("simulate pre-migration row: %v", err)
	}

	got, err := st.GetSubmission(ctx, "sub_pre260")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.Secrets != nil {
		t.Errorf("Secrets = %+v, want nil", got.Secrets)
	}
	if got.SecretNames != nil {
		t.Errorf("SecretNames = %v, want nil", got.SecretNames)
	}
	if got.SecretsPurgedAt != nil {
		t.Errorf("SecretsPurgedAt = %v, want nil", got.SecretsPurgedAt)
	}
}

func TestPurgeSubmissionSecrets(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	secrets := map[string]string{"HF_TOKEN": "abc"}
	if err := st.CreateSubmission(ctx, secretsSubmission("sub_purge", secrets)); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	purgedAt := time.Now().UTC().Truncate(time.Second)
	if err := st.PurgeSubmissionSecrets(ctx, "sub_purge", purgedAt); err != nil {
		t.Fatalf("purge: %v", err)
	}

	if raw := rawSubmissionSecrets(t, st, "sub_purge"); raw != nil {
		t.Errorf("expected secrets column NULL after purge, got %v", raw)
	}

	got, err := st.GetSubmission(ctx, "sub_purge")
	if err != nil {
		t.Fatalf("get submission: %v", err)
	}
	if got.Secrets != nil {
		t.Errorf("Secrets = %+v, want nil after purge", got.Secrets)
	}
	if len(got.SecretNames) != 1 || got.SecretNames[0] != "HF_TOKEN" {
		t.Errorf("SecretNames = %v, want [HF_TOKEN] retained after purge", got.SecretNames)
	}
	if got.SecretsPurgedAt == nil || !got.SecretsPurgedAt.Equal(purgedAt) {
		t.Errorf("SecretsPurgedAt = %v, want %v", got.SecretsPurgedAt, purgedAt)
	}
	if got.SecretsState() != "purged" {
		t.Errorf("SecretsState() = %q, want purged", got.SecretsState())
	}
}

func TestPurgeSubmissionSecretsNotFound(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.PurgeSubmissionSecrets(ctx, "sub_missing", time.Now()); err == nil {
		t.Fatal("expected error purging a nonexistent submission")
	}
}

func TestSubmissionSecretsFailClosedWithoutKey(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(nil, true) // no cipher, refuse plaintext
	ctx := context.Background()

	sub := secretsSubmission("sub_fail", map[string]string{"HF_TOKEN": "abc"})
	if err := st.CreateSubmission(ctx, sub); err == nil {
		t.Fatal("expected fail-closed error persisting secrets without a key, got nil")
	}
	// A submission with no secrets must still succeed.
	if err := st.CreateSubmission(ctx, secretsSubmission("sub_empty", nil)); err != nil {
		t.Fatalf("no-secrets submission should succeed: %v", err)
	}
}

func TestSubmissionSecretsPlaintextAllowed(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(nil, false) // legacy plaintext mode
	ctx := context.Background()

	if err := st.CreateSubmission(ctx, secretsSubmission("sub_plain", map[string]string{"HF_TOKEN": "plain-secret"})); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	raw := rawSubmissionSecrets(t, st, "sub_plain")
	if raw == nil || !strings.Contains(*raw, "plain-secret") {
		t.Fatalf("expected plaintext at rest, got %v", raw)
	}
}

func TestSubmissionSecretsReadFailsClosedWithoutKey(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	if err := st.CreateSubmission(ctx, secretsSubmission("sub_rfc", map[string]string{"HF_TOKEN": "abc"})); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Simulate a restart with the key removed/misconfigured.
	st.ConfigureTokenEncryption(nil, true)
	if _, err := st.GetSubmission(ctx, "sub_rfc"); err == nil {
		t.Fatal("GetSubmission with encrypted secrets and no key: expected error, got nil")
	}
}

func TestTaskRuntimeHintsSecretsEncryptedAtRest(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	secrets := map[string]string{"HF_TOKEN": "worker-secret-value"}
	task := secretsTask("task_secrets_enc", secrets)
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The caller's in-memory task must NOT be mutated to ciphertext.
	if task.RuntimeHints.Secrets["HF_TOKEN"] != "worker-secret-value" {
		t.Fatalf("CreateTask mutated in-memory secrets: %v", task.RuntimeHints.Secrets)
	}

	raw := rawTaskHints(t, st, "task_secrets_enc")
	if strings.Contains(raw, "worker-secret-value") {
		t.Fatalf("stored runtime_hints leaks plaintext secret: %q", raw)
	}
	if !strings.Contains(raw, "__enc__") {
		t.Fatalf("stored runtime_hints missing __enc__ wrapper: %q", raw)
	}
	if !strings.Contains(raw, "enc:v2:") {
		t.Fatalf("stored runtime_hints secrets not encrypted (expected v2/AAD-bound): %q", raw)
	}

	got, err := st.GetTask(ctx, "task_secrets_enc")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.RuntimeHints.Secrets["HF_TOKEN"] != "worker-secret-value" {
		t.Fatalf("decrypted task secrets mismatch: %+v", got.RuntimeHints.Secrets)
	}
}

func TestTaskRuntimeHintsSecretsAndCredentialBothEncrypt(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	task := secretsTask("task_both", map[string]string{"A": "a-value"})
	task.RuntimeHints.StagerOverrides = &model.StagerOverrides{
		HTTPCredential: &model.HTTPCredential{Type: "bearer", Token: "bearer-value"},
	}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	got, err := st.GetTask(ctx, "task_both")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.RuntimeHints.Secrets["A"] != "a-value" {
		t.Errorf("Secrets[A] = %q, want a-value", got.RuntimeHints.Secrets["A"])
	}
	if got.RuntimeHints.StagerOverrides.HTTPCredential.Token != "bearer-value" {
		t.Errorf("HTTPCredential.Token = %q, want bearer-value", got.RuntimeHints.StagerOverrides.HTTPCredential.Token)
	}
}

func TestListSubmissionsWithSecretsForRetention(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	// A terminal submission with secrets: must be listed.
	completedSub := secretsSubmission("sub_ret_completed", map[string]string{"A": "1"})
	completedSub.State = model.SubmissionStateCompleted
	completedAt := time.Now().UTC().Truncate(time.Second)
	completedSub.CompletedAt = &completedAt
	if err := st.CreateSubmission(ctx, completedSub); err != nil {
		t.Fatalf("create completed submission: %v", err)
	}

	// A non-terminal submission with secrets: must NOT be listed.
	runningSub := secretsSubmission("sub_ret_running", map[string]string{"A": "1"})
	runningSub.State = model.SubmissionStateRunning
	if err := st.CreateSubmission(ctx, runningSub); err != nil {
		t.Fatalf("create running submission: %v", err)
	}

	// A terminal submission with no secrets: must NOT be listed.
	noSecretsSub := secretsSubmission("sub_ret_nosecrets", nil)
	noSecretsSub.State = model.SubmissionStateFailed
	if err := st.CreateSubmission(ctx, noSecretsSub); err != nil {
		t.Fatalf("create no-secrets submission: %v", err)
	}

	// A terminal submission already purged: must NOT be listed.
	purgedSub := secretsSubmission("sub_ret_purged", map[string]string{"A": "1"})
	purgedSub.State = model.SubmissionStateCancelled
	if err := st.CreateSubmission(ctx, purgedSub); err != nil {
		t.Fatalf("create purged submission: %v", err)
	}
	if err := st.PurgeSubmissionSecrets(ctx, "sub_ret_purged", time.Now()); err != nil {
		t.Fatalf("purge: %v", err)
	}

	results, err := st.ListSubmissionsWithSecretsForRetention(ctx)
	if err != nil {
		t.Fatalf("list for retention: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result, got %d: %+v", len(results), results)
	}
	got := results[0]
	if got.ID != "sub_ret_completed" {
		t.Errorf("ID = %q, want sub_ret_completed", got.ID)
	}
	if got.Secrets != nil {
		t.Errorf("Secrets = %+v, want nil (values must not be decrypted here)", got.Secrets)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(completedAt) {
		t.Errorf("CompletedAt = %v, want %v", got.CompletedAt, completedAt)
	}
	if got.SecretsRetention != "keep" {
		t.Errorf("SecretsRetention = %q, want keep", got.SecretsRetention)
	}
}

func TestReencryptPlaintextTokensCoversSecrets(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	// Write rows in legacy plaintext mode.
	st.ConfigureTokenEncryption(nil, false)
	if err := st.CreateSubmission(ctx, secretsSubmission("sub_secrets_mig", map[string]string{"A": "legacy-a"})); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	if err := st.CreateTask(ctx, secretsTask("task_secrets_mig", map[string]string{"A": "legacy-task-a"})); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Enable encryption and migrate.
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	nSub, nTask, err := st.ReencryptPlaintextTokens(ctx)
	if err != nil {
		t.Fatalf("reencrypt: %v", err)
	}
	if nSub != 1 {
		t.Errorf("expected 1 submission rewritten, got %d", nSub)
	}
	if nTask != 1 {
		t.Errorf("expected 1 task rewritten, got %d", nTask)
	}

	if raw := rawSubmissionSecrets(t, st, "sub_secrets_mig"); raw == nil || !tokencrypt.IsEncrypted(*raw) {
		t.Fatalf("submission secrets not upgraded: %v", raw)
	}
	if raw := rawTaskHints(t, st, "task_secrets_mig"); strings.Contains(raw, "legacy-task-a") {
		t.Fatalf("task secrets not upgraded: %q", raw)
	}

	sub, err := st.GetSubmission(ctx, "sub_secrets_mig")
	if err != nil || sub.Secrets["A"] != "legacy-a" {
		t.Fatalf("submission secrets not readable post-migration: %v / %+v", err, sub.Secrets)
	}
	task, err := st.GetTask(ctx, "task_secrets_mig")
	if err != nil || task.RuntimeHints.Secrets["A"] != "legacy-task-a" {
		t.Fatalf("task secrets not readable post-migration: %v", err)
	}

	// Idempotent: a second pass rewrites nothing.
	nSub2, nTask2, err := st.ReencryptPlaintextTokens(ctx)
	if err != nil || nSub2 != 0 || nTask2 != 0 {
		t.Fatalf("second migration not idempotent: (%d,%d) err=%v", nSub2, nTask2, err)
	}
}
