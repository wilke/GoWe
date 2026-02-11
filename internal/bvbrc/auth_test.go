package bvbrc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveToken_EnvVar(t *testing.T) {
	t.Setenv("BVBRC_TOKEN", "env-token-value")
	tok, err := ResolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "env-token-value" {
		t.Errorf("got %q, want %q", tok, "env-token-value")
	}
}

func TestResolveToken_GoWeCredentials(t *testing.T) {
	t.Setenv("BVBRC_TOKEN", "") // clear env
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	goweDir := filepath.Join(dir, ".gowe")
	if err := os.MkdirAll(goweDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goweDir, "credentials.json"), []byte(`{"token":"cred-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, err := ResolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "cred-token" {
		t.Errorf("got %q, want %q", tok, "cred-token")
	}
}

func TestResolveToken_BVBRCTokenFile(t *testing.T) {
	t.Setenv("BVBRC_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := os.WriteFile(filepath.Join(dir, ".bvbrc_token"), []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tok, err := ResolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "file-token" {
		t.Errorf("got %q, want %q", tok, "file-token")
	}
}

func TestResolveToken_NoToken(t *testing.T) {
	t.Setenv("BVBRC_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	_, err := ResolveToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseToken_Valid(t *testing.T) {
	expiry := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC).Unix()
	raw := "un=testuser|tokenid=abc-123|expiry=" + time.Unix(expiry, 0).Format("1136239445")
	// Use the actual numeric value.
	raw = "un=testuser|tokenid=abc-123|expiry=1775952000|client_id=x|token_type=Bearer|sig=xyz"

	info := ParseToken(raw)
	if info.Username != "testuser" {
		t.Errorf("Username = %q, want %q", info.Username, "testuser")
	}
	if info.Expiry.Unix() != 1775952000 {
		t.Errorf("Expiry = %v, want Unix 1775952000", info.Expiry)
	}
	if info.Raw != raw {
		t.Errorf("Raw not preserved")
	}
}

func TestParseToken_Empty(t *testing.T) {
	info := ParseToken("")
	if info.Username != "" {
		t.Errorf("Username = %q, want empty", info.Username)
	}
	if !info.Expiry.IsZero() {
		t.Errorf("Expiry = %v, want zero", info.Expiry)
	}
}

func TestTokenInfo_IsExpired(t *testing.T) {
	tests := []struct {
		name    string
		expiry  time.Time
		expired bool
	}{
		{"past", time.Now().Add(-time.Hour), true},
		{"future", time.Now().Add(time.Hour), false},
		{"zero", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := TokenInfo{Expiry: tt.expiry}
			if got := info.IsExpired(); got != tt.expired {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expired)
			}
		})
	}
}
