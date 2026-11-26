package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// sampleServiceToken returns a token of name for the raw value raw.
func sampleServiceToken(id, name, raw string) *model.ServiceToken {
	return &model.ServiceToken{
		ID:          id,
		Name:        name,
		TokenHash:   model.HashServiceToken(raw),
		TokenPrefix: raw[:min(len(raw), model.ServiceTokenDisplayPrefixLen)],
		Description: "router in prod",
		CreatedBy:   "admin",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
}

// Create, look up by hash, list, touch and delete, on the store the factory
// returns (SQLite, or PostgreSQL when GOWE_TEST_POSTGRES_URL is set).
func TestServiceTokenCRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	raw := "gowe_svc_" + strings.Repeat("a", 43)
	exp := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	tok := sampleServiceToken("svt_1", "model-router", raw)
	tok.ExpiresAt = &exp
	if err := st.CreateServiceToken(ctx, tok); err != nil {
		t.Fatalf("CreateServiceToken: %v", err)
	}

	got, err := st.GetServiceTokenByHash(ctx, model.HashServiceToken(raw))
	if err != nil || got == nil {
		t.Fatalf("GetServiceTokenByHash = %v, %v", got, err)
	}
	if got.ID != "svt_1" || got.Name != "model-router" || got.TokenPrefix != raw[:16] ||
		got.Description != "router in prod" || got.CreatedBy != "admin" || !got.CreatedAt.Equal(tok.CreatedAt) {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if got.TokenHash != model.HashServiceToken(raw) {
		t.Error("GetServiceTokenByHash must return the stored hash")
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, exp)
	}
	if got.LastUsedAt != nil {
		t.Errorf("LastUsedAt = %v, want nil", got.LastUsedAt)
	}

	// The raw token is stored nowhere.
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM service_tokens WHERE token_hash = ? OR token_prefix = ? OR description = ?`,
		raw, raw, raw).Scan(&n); err != nil || n != 0 {
		t.Errorf("raw token found in %d rows (%v)", n, err)
	}

	if k, err := st.GetServiceTokenByHash(ctx, model.HashServiceToken("gowe_svc_unknown")); err != nil || k != nil {
		t.Errorf("unknown hash = %v, %v; want nil, nil", k, err)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := st.TouchServiceToken(ctx, "svt_1", when); err != nil {
		t.Fatalf("TouchServiceToken: %v", err)
	}
	got, _ = st.GetServiceTokenByHash(ctx, model.HashServiceToken(raw))
	if got == nil || got.LastUsedAt == nil || !got.LastUsedAt.Equal(when) {
		t.Errorf("LastUsedAt after touch = %+v, want %v", got, when)
	}
	if err := st.TouchServiceToken(ctx, "svt_missing", when); err != nil {
		t.Errorf("touching a missing token: %v, want nil", err)
	}

	// A second token of the same name (rotation), no expiry.
	raw2 := "gowe_svc_" + strings.Repeat("b", 43)
	if err := st.CreateServiceToken(ctx, sampleServiceToken("svt_2", "model-router", raw2)); err != nil {
		t.Fatalf("second token of the same name: %v", err)
	}
	list, err := st.ListServiceTokens(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListServiceTokens = %d tokens, %v; want 2", len(list), err)
	}
	for _, lt := range list {
		if lt.TokenHash != "" {
			t.Errorf("ListServiceTokens exposed the hash of %s", lt.ID)
		}
	}
	data, _ := json.Marshal(list)
	if strings.Contains(string(data), model.HashServiceToken(raw)) || strings.Contains(string(data), "token_hash") {
		t.Errorf("the listed tokens serialize the hash: %s", data)
	}

	name, deleted, err := st.DeleteServiceToken(ctx, "svt_1")
	if err != nil || !deleted || name != "model-router" {
		t.Fatalf("DeleteServiceToken = %q, %v, %v; want model-router, true", name, deleted, err)
	}
	if k, _ := st.GetServiceTokenByHash(ctx, model.HashServiceToken(raw)); k != nil {
		t.Error("a deleted token is still found by its hash")
	}
	if name, deleted, err := st.DeleteServiceToken(ctx, "svt_1"); err != nil || deleted || name != "" {
		t.Errorf("deleting again = %q, %v, %v; want \"\", false, nil", name, deleted, err)
	}
	if list, _ := st.ListServiceTokens(ctx); len(list) != 1 || list[0].ID != "svt_2" {
		t.Errorf("after delete: %+v", list)
	}
}

// expires_at: NULL is no expiry; an empty or unparsable value is an invalid
// expiry (expired), never "no expiry", and the row is still read and listed
// so it can be revoked.
func TestServiceToken_InvalidStoredExpiry(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	cases := []struct {
		id, stored  string
		null        bool
		wantInvalid bool
	}{
		{id: "svt_null", null: true},
		{id: "svt_empty", stored: "", wantInvalid: true},
		{id: "svt_garbage", stored: "next tuesday", wantInvalid: true},
		{id: "svt_valid", stored: "2999-01-02T03:04:05Z"},
	}
	for i, c := range cases {
		raw := fmt.Sprintf("gowe_svc_%043d", i)
		if err := st.CreateServiceToken(ctx, sampleServiceToken(c.id, "router", raw)); err != nil {
			t.Fatalf("%s: CreateServiceToken: %v", c.id, err)
		}
		if !c.null {
			if _, err := st.db.ExecContext(ctx, `UPDATE service_tokens SET expires_at = ? WHERE id = ?`, c.stored, c.id); err != nil {
				t.Fatalf("%s: set expires_at: %v", c.id, err)
			}
		}
		got, err := st.GetServiceTokenByHash(ctx, model.HashServiceToken(raw))
		if err != nil || got == nil {
			t.Fatalf("%s: GetServiceTokenByHash = %v, %v; want the token (not an error)", c.id, got, err)
		}
		if got.ExpiryInvalid != c.wantInvalid {
			t.Errorf("%s: ExpiryInvalid = %v, want %v", c.id, got.ExpiryInvalid, c.wantInvalid)
		}
		now := time.Now()
		switch {
		case c.null:
			if got.ExpiresAt != nil || got.Expired(now) {
				t.Errorf("%s: ExpiresAt %v, expired %v; want no expiry", c.id, got.ExpiresAt, got.Expired(now))
			}
		case c.wantInvalid:
			if got.ExpiresAt == nil || !got.Expired(now) {
				t.Errorf("%s: ExpiresAt %v, expired %v; want a non-nil expiry and expired", c.id, got.ExpiresAt, got.Expired(now))
			}
		default:
			if got.ExpiresAt == nil || got.ExpiresAt.Year() != 2999 || got.Expired(now) {
				t.Errorf("%s: ExpiresAt %v, expired %v", c.id, got.ExpiresAt, got.Expired(now))
			}
		}
	}

	list, err := st.ListServiceTokens(ctx)
	if err != nil || len(list) != len(cases) {
		t.Fatalf("ListServiceTokens = %d tokens, %v; want %d (invalid rows are listed, not skipped)", len(list), err, len(cases))
	}
	for _, lt := range list {
		if (lt.ID == "svt_empty" || lt.ID == "svt_garbage") && (!lt.ExpiryInvalid || lt.ExpiresAt == nil) {
			t.Errorf("listed %s: %+v, want an invalid, non-nil expiry", lt.ID, lt)
		}
	}
}

// The hash is unique; a failed insert stores nothing.
func TestServiceToken_UniqueHash(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	raw := "gowe_svc_" + strings.Repeat("c", 43)
	if err := st.CreateServiceToken(ctx, sampleServiceToken("svt_a", "a", raw)); err != nil {
		t.Fatal(err)
	}
	err := st.CreateServiceToken(ctx, sampleServiceToken("svt_b", "b", raw))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate hash: err = %v, want ErrDuplicate", err)
	}
	if strings.Contains(err.Error(), model.HashServiceToken(raw)) {
		t.Errorf("the error names the hash: %v", err)
	}
	if list, _ := st.ListServiceTokens(ctx); len(list) != 1 {
		t.Errorf("%d tokens stored, want 1", len(list))
	}
}

// At most MaxServiceTokensPerName tokens per name and MaxServiceTokens in
// total.
func TestServiceToken_Limits(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mk := func(i int, name string) *model.ServiceToken {
		return sampleServiceToken(fmt.Sprintf("svt_%04d", i), name, fmt.Sprintf("gowe_svc_%043d", i))
	}
	i := 0
	for ; i < model.MaxServiceTokensPerName; i++ {
		if err := st.CreateServiceToken(ctx, mk(i, "router")); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if err := st.CreateServiceToken(ctx, mk(i, "router")); !errors.Is(err, ErrServiceTokenLimit) {
		t.Fatalf("token %d of one name: err = %v, want ErrServiceTokenLimit", i+1, err)
	}
	// Other names fill the server up to the total.
	for ; i < model.MaxServiceTokens; i++ {
		if err := st.CreateServiceToken(ctx, mk(i, fmt.Sprintf("svc%d", i))); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if err := st.CreateServiceToken(ctx, mk(i, "fresh")); !errors.Is(err, ErrServiceTokenLimit) {
		t.Fatalf("token %d in total: err = %v, want ErrServiceTokenLimit", i+1, err)
	}
	if list, _ := st.ListServiceTokens(ctx); len(list) != model.MaxServiceTokens {
		t.Errorf("%d tokens stored, want %d", len(list), model.MaxServiceTokens)
	}
	// Deleting one makes room again.
	if _, ok, err := st.DeleteServiceToken(ctx, "svt_0000"); err != nil || !ok {
		t.Fatalf("delete svt_0000 = %v, %v", ok, err)
	}
	if err := st.CreateServiceToken(ctx, mk(i, "router")); err != nil {
		t.Errorf("after a delete: %v", err)
	}
}
