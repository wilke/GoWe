package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

func TestStore_RefusesInjectWithoutBlessingID(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	bad := tokenTask("task_bad", "tok")
	bad.RuntimeHints.TokenBlessingID = ""
	if err := st.CreateTask(ctx, bad); !errors.Is(err, model.ErrUnblessedInject) {
		t.Fatalf("CreateTask(inject, no blessing) err = %v, want ErrUnblessedInject", err)
	}

	good := tokenTask("task_good", "tok") // has TokenBlessingID
	if err := st.CreateTask(ctx, good); err != nil {
		t.Fatalf("blessed inject must persist: %v", err)
	}

	// Update path: dropping the blessing id on a live task is refused.
	good.RuntimeHints.TokenBlessingID = ""
	if err := st.UpdateTask(ctx, good); !errors.Is(err, model.ErrUnblessedInject) {
		t.Fatalf("UpdateTask(inject, no blessing) err = %v, want ErrUnblessedInject", err)
	}

	// Terminal rows are exempt (a failed/cancelled row must always be writable).
	good.State = model.TaskStateFailed
	if err := st.UpdateTask(ctx, good); err != nil {
		t.Fatalf("terminal task must be writable: %v", err)
	}
	term := tokenTask("task_term", "tok")
	term.RuntimeHints.TokenBlessingID = ""
	term.State = model.TaskStateFailed
	if err := st.CreateTask(ctx, term); err != nil {
		t.Fatalf("terminal create must be writable: %v", err)
	}

	// No inject flag: unaffected.
	plain := tokenTask("task_plain", "tok")
	plain.RuntimeHints.InjectBVBRCToken = false
	plain.RuntimeHints.TokenBlessingID = ""
	if err := st.CreateTask(ctx, plain); err != nil {
		t.Fatalf("non-inject task must persist: %v", err)
	}
}

// TestScrubTerminalRaw_ClearsStaleRows_IncludingUndecryptable_Idempotent seeds
// terminal rows that carry a credential encrypted under the CURRENT key, under
// a DIFFERENT key, and a non-terminal row, plus a row whose runtime_hints is
// not valid JSON. The sweep must clear exactly the terminal credential rows
// (no decryption), leave the live row and the invalid row alone, and be a
// no-op when run again.
func TestScrubTerminalRaw_ClearsStaleRows_IncludingUndecryptable_Idempotent(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()

	mk := func(id string, state model.TaskState, withSecret bool) {
		task := tokenTask(id, "secret-"+id)
		task.State = state
		if withSecret {
			task.RuntimeHints.Secrets = map[string]string{"HF_TOKEN": "hf-" + id}
		}
		if err := st.CreateTask(ctx, task); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("t_failed", model.TaskStateFailed, true)
	mk("t_success", model.TaskStateSuccess, false)
	mk("t_skipped", model.TaskStateSkipped, false)
	mk("t_live", model.TaskStateRunning, false)

	// A terminal row encrypted under a different key: rewrite the stored
	// blob with a ciphertext no current-key decrypt can open.
	otherKey := make([]byte, 32)
	for i := range otherKey {
		otherKey[i] = byte(200 - i)
	}
	other, err := tokencrypt.New(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	mk("t_oldkey", model.TaskStateFailed, false)
	st.ConfigureTokenEncryption(other, true)
	enc, err := other.Encrypt("secret-t_oldkey", taskHintsAAD("t_oldkey"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints=json_set(runtime_hints,'$.stager_overrides.http_credential.token',?) WHERE id='t_oldkey'`, enc); err != nil {
		t.Fatal(err)
	}
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	if _, err := st.GetTask(ctx, "t_oldkey"); err == nil {
		t.Log("note: GetTask decrypted an old-key row; sweep is still raw")
	}

	// A terminal row with invalid JSON in runtime_hints.
	mk("t_badjson", model.TaskStateFailed, false)
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints='' WHERE id='t_badjson'`); err != nil {
		t.Fatal(err)
	}

	n, err := st.ScrubTerminalTaskCredentialsRaw(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 4 { // failed, success, skipped, oldkey
		t.Errorf("rows scrubbed = %d, want 4", n)
	}

	for _, id := range []string{"t_failed", "t_success", "t_skipped", "t_oldkey"} {
		raw := rawTaskHints(t, st, id)
		if strings.Contains(raw, "http_credential") || strings.Contains(raw, `"secrets"`) || strings.Contains(raw, "enc:v") {
			t.Errorf("%s still holds credentials after sweep: %s", id, raw)
		}
		// metadata survives
		if !strings.Contains(raw, "token_blessing_id") {
			t.Errorf("%s lost its metadata: %s", id, raw)
		}
	}
	if raw := rawTaskHints(t, st, "t_live"); !strings.Contains(raw, "http_credential") {
		t.Errorf("non-terminal row must keep its credential: %s", raw)
	}
	if raw := rawTaskHints(t, st, "t_badjson"); raw != "" {
		t.Errorf("invalid-JSON row must be left alone: %q", raw)
	}

	// Idempotent.
	n, err = st.ScrubTerminalTaskCredentialsRaw(ctx)
	if err != nil || n != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", n, err)
	}
}
