package store

import (
	"context"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// seedPreUpgradeQueued writes a QUEUED worker task as a pre-policy server
// would have: inject flag, credential, no blessing id (raw SQL bypasses the
// store guard, as the old code did).
func seedPreUpgradeQueued(t *testing.T, st *SQLiteStore, id string) {
	t.Helper()
	ctx := context.Background()
	task := tokenTask(id, "tok-"+id)
	task.RuntimeHints.TokenBlessingID = "tb_seed"
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints=json_remove(runtime_hints,'$.token_blessing_id') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
}

func TestClearUnblessedInjectRaw_QueuedRow(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()
	seedPreUpgradeQueued(t, st, "t_old")
	good := tokenTask("t_good", "tok") // blessed, must be untouched
	if err := st.CreateTask(ctx, good); err != nil {
		t.Fatal(err)
	}
	// invalid JSON row must not break the sweep
	plain := tokenTask("t_bad", "tok")
	plain.RuntimeHints.InjectBVBRCToken = false
	plain.RuntimeHints.TokenBlessingID = ""
	if err := st.CreateTask(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints='' WHERE id='t_bad'`); err != nil {
		t.Fatal(err)
	}

	n, err := st.ClearUnblessedInjectRaw(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep = (%d, %v), want (1, nil)", n, err)
	}
	if raw := rawTaskHints(t, st, "t_old"); strings.Contains(raw, "inject_bvbrc_token") {
		t.Errorf("request not cleared: %s", raw)
	}
	if raw := rawTaskHints(t, st, "t_good"); !strings.Contains(raw, "inject_bvbrc_token") {
		t.Errorf("blessed row must be untouched: %s", raw)
	}
	if n, err := st.ClearUnblessedInjectRaw(ctx); err != nil || n != 0 {
		t.Errorf("second sweep = (%d, %v)", n, err)
	}
}

func TestCheckoutTask_NeverHandsOutUnblessedInjectRow(t *testing.T) {
	st := testStore(t)
	st.ConfigureTokenEncryption(testTokenCipher(t), true)
	ctx := context.Background()
	seedPreUpgradeQueued(t, st, "t_old")
	// a row with invalid JSON hints must not make the query error
	plain := tokenTask("t_plain", "tok")
	plain.RuntimeHints = nil
	if err := st.CreateTask(ctx, plain); err != nil {
		t.Fatal(err)
	}
	w := sampleWorker()
	if err := st.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "t_plain" {
		t.Fatalf("checked out %+v, want only t_plain", got)
	}
	if again, err := st.CheckoutTask(ctx, w.ID, "", model.RuntimeNone); err != nil || again != nil {
		t.Fatalf("the unblessed-inject row must never be checked out: %+v %v", again, err)
	}
}
