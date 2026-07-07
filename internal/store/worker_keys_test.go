package store

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func sampleWorkerKey(id, raw string) *model.WorkerKey {
	return &model.WorkerKey{
		ID:          id,
		Label:       "esmfold-node-1",
		KeyHash:     model.HashWorkerKey(raw),
		KeyPrefix:   model.WorkerKeyDisplayPrefix(raw),
		Groups:      []string{"esmfold", "default"},
		Description: "test key",
		CreatedBy:   "admin",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
}

func TestWorkerKeyCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	raw := "gwk_rawsecret123"
	key := sampleWorkerKey("wk_1", raw)

	if err := st.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("CreateWorkerKey: %v", err)
	}

	// Count reflects the new key.
	if n, err := st.CountWorkerKeys(ctx); err != nil || n != 1 {
		t.Fatalf("CountWorkerKeys = %d, %v; want 1, nil", n, err)
	}

	// Lookup by hash (the auth path).
	got, err := st.GetWorkerKeyByHash(ctx, model.HashWorkerKey(raw))
	if err != nil {
		t.Fatalf("GetWorkerKeyByHash: %v", err)
	}
	if got == nil {
		t.Fatal("GetWorkerKeyByHash returned nil for existing key")
	}
	if got.ID != "wk_1" || got.KeyPrefix != model.WorkerKeyDisplayPrefix(raw) {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "esmfold" {
		t.Errorf("groups roundtrip mismatch: %v", got.Groups)
	}

	// The raw secret must never be stored — only its hash.
	if got.KeyHash != model.HashWorkerKey(raw) {
		t.Errorf("stored hash mismatch")
	}

	// Lookup by ID.
	byID, err := st.GetWorkerKeyByID(ctx, "wk_1")
	if err != nil || byID == nil {
		t.Fatalf("GetWorkerKeyByID: %v, %v", byID, err)
	}

	// Unknown hash returns nil, not error.
	if k, err := st.GetWorkerKeyByHash(ctx, "deadbeef"); err != nil || k != nil {
		t.Errorf("unknown hash: got %v, %v; want nil, nil", k, err)
	}
}

func TestWorkerKeyUpdateAndTouch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	raw := "gwk_updateme"
	key := sampleWorkerKey("wk_upd", raw)
	if err := st.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("CreateWorkerKey: %v", err)
	}

	// Disable + change expiry.
	exp := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	key.Disabled = true
	key.ExpiresAt = &exp
	key.Groups = []string{"only-this"}
	if err := st.UpdateWorkerKey(ctx, key); err != nil {
		t.Fatalf("UpdateWorkerKey: %v", err)
	}

	got, _ := st.GetWorkerKeyByID(ctx, "wk_upd")
	if !got.Disabled {
		t.Errorf("Disabled not persisted")
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
	if len(got.Groups) != 1 || got.Groups[0] != "only-this" {
		t.Errorf("Groups update not persisted: %v", got.Groups)
	}

	// Touch records last-used.
	when := time.Now().UTC().Truncate(time.Second)
	if err := st.TouchWorkerKey(ctx, "wk_upd", when); err != nil {
		t.Fatalf("TouchWorkerKey: %v", err)
	}
	got, _ = st.GetWorkerKeyByID(ctx, "wk_upd")
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(when) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, when)
	}
}

func TestWorkerKeyDeleteAndList(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_a", "gwk_a")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_b", "gwk_b")); err != nil {
		t.Fatal(err)
	}

	keys, err := st.ListWorkerKeys(ctx)
	if err != nil {
		t.Fatalf("ListWorkerKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("ListWorkerKeys len = %d, want 2", len(keys))
	}

	if err := st.DeleteWorkerKey(ctx, "wk_a"); err != nil {
		t.Fatalf("DeleteWorkerKey: %v", err)
	}
	if n, _ := st.CountWorkerKeys(ctx); n != 1 {
		t.Errorf("after delete count = %d, want 1", n)
	}

	// Deleting a missing key is an error (surfaces as 404 in the handler).
	if err := st.DeleteWorkerKey(ctx, "wk_missing"); err == nil {
		t.Errorf("DeleteWorkerKey(missing) = nil, want error")
	}
}
