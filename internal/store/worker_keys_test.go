package store

import (
	"context"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// newTestStore is testStore under the name the worker-key and tool-blessing
// tests use.
func newTestStore(t *testing.T) *SQLStore {
	t.Helper()
	return testStore(t)
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

// Creating a key sets worker_auth.required; deleting every key leaves it.
func TestCreateWorkerKey_SetsWorkerAuthRequired(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, found, err := st.GetServerSetting(ctx, WorkerAuthRequiredKey); err != nil || found {
		t.Fatalf("fresh store: found=%v err=%v; want no setting", found, err)
	}
	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_a", "gwk_a")); err != nil {
		t.Fatal(err)
	}
	if v, found, err := st.GetServerSetting(ctx, WorkerAuthRequiredKey); err != nil || !found || v != "true" {
		t.Fatalf("after create = %q, %v, %v; want \"true\"", v, found, err)
	}
	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_b", "gwk_b")); err != nil {
		t.Fatalf("second key: %v", err)
	}
	for _, id := range []string{"wk_a", "wk_b"} {
		if err := st.DeleteWorkerKey(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if v, found, err := st.GetServerSetting(ctx, WorkerAuthRequiredKey); err != nil || !found || v != "true" {
		t.Errorf("after deleting every key = %q, %v, %v; want \"true\"", v, found, err)
	}
	// A failed insert (duplicate hash) sets nothing new and stores no key.
	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_c", "gwk_c")); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkerKey(ctx, sampleWorkerKey("wk_d", "gwk_c")); err == nil {
		t.Error("a duplicate key hash must fail")
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_d"); k != nil {
		t.Error("the failed key was stored")
	}
}

// DeleteWorkerKeyForGroup deletes only a key whose groups are exactly
// [group].
func TestDeleteWorkerKeyForGroup(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for id, groups := range map[string][]string{
		"wk_g":   {"g"},
		"wk_gh":  {"g", "h"},
		"wk_h":   {"h"},
		"wk_any": {},
		"wk_gg":  {"g", "g"},
	} {
		k := sampleWorkerKey(id, "gwk_"+id)
		k.Groups = groups
		if err := st.CreateWorkerKey(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"wk_gh", "wk_h", "wk_any", "wk_gg", "wk_missing"} {
		if deleted, err := st.DeleteWorkerKeyForGroup(ctx, id, "g"); err != nil || deleted {
			t.Errorf("DeleteWorkerKeyForGroup(%s, g) = %v, %v; want false, nil", id, deleted, err)
		}
	}
	if n, _ := st.CountWorkerKeys(ctx); n != 5 {
		t.Fatalf("%d keys left, want all 5", n)
	}
	if deleted, err := st.DeleteWorkerKeyForGroup(ctx, "wk_g", "h"); err != nil || deleted {
		t.Errorf("DeleteWorkerKeyForGroup(wk_g, h) = %v, %v; want false, nil", deleted, err)
	}
	if deleted, err := st.DeleteWorkerKeyForGroup(ctx, "wk_g", "g"); err != nil || !deleted {
		t.Fatalf("DeleteWorkerKeyForGroup(wk_g, g) = %v, %v; want true, nil", deleted, err)
	}
	if k, _ := st.GetWorkerKeyByID(ctx, "wk_g"); k != nil {
		t.Error("wk_g is still stored")
	}
	if n, _ := st.CountWorkerKeys(ctx); n != 4 {
		t.Errorf("%d keys left, want 4", n)
	}
}
