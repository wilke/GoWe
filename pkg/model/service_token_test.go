package model

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestGenerateServiceToken_Format(t *testing.T) {
	raw, hash, prefix, err := GenerateServiceToken()
	if err != nil {
		t.Fatalf("GenerateServiceToken: %v", err)
	}
	if !strings.HasPrefix(raw, ServiceTokenRawPrefix) {
		t.Errorf("token %q lacks the prefix %q", raw[:ServiceTokenDisplayPrefixLen], ServiceTokenRawPrefix)
	}
	if len(raw) != len("gowe_svc_")+43 || len(raw) != ServiceTokenLen {
		t.Errorf("token length = %d, want %d", len(raw), len("gowe_svc_")+43)
	}
	secret := strings.TrimPrefix(raw, ServiceTokenRawPrefix)
	b, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(b) != 32 {
		t.Errorf("secret part decodes to %d bytes (%v), want 32", len(b), err)
	}
	if !WellFormedServiceToken(raw) {
		t.Error("a generated token is not well formed")
	}
	if hash != HashServiceToken(raw) || len(hash) != 64 || hash != strings.ToLower(hash) {
		t.Errorf("hash = %q, want the lower-case hex sha256 of the token", hash)
	}
	if prefix != raw[:16] || len(prefix) != 16 {
		t.Errorf("prefix = %q, want the first 16 characters", prefix)
	}
}

func TestGenerateServiceToken_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		raw, hash, _, err := GenerateServiceToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[raw] || seen[hash] {
			t.Fatalf("duplicate token after %d tokens", i)
		}
		seen[raw], seen[hash] = true, true
	}
}

func TestWellFormedServiceToken(t *testing.T) {
	good := ServiceTokenRawPrefix + strings.Repeat("A", 43)
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"valid", good, true},
		{"valid with - and _", ServiceTokenRawPrefix + strings.Repeat("-_aZ09", 7) + "x", true},
		{"one short", good[:len(good)-1], false},
		{"one long", good + "A", false},
		{"no prefix", strings.Repeat("A", ServiceTokenLen), false},
		{"wrong prefix case", "GOWE_SVC_" + strings.Repeat("A", 43), false},
		{"padding", ServiceTokenRawPrefix + strings.Repeat("A", 42) + "=", false},
		{"standard base64 +", ServiceTokenRawPrefix + strings.Repeat("A", 42) + "+", false},
		{"slash", ServiceTokenRawPrefix + strings.Repeat("A", 42) + "/", false},
		{"space", ServiceTokenRawPrefix + strings.Repeat("A", 42) + " ", false},
		{"pipe", ServiceTokenRawPrefix + strings.Repeat("A", 42) + "|", false},
		{"empty", "", false},
		{"prefix only", ServiceTokenRawPrefix, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WellFormedServiceToken(tt.raw); got != tt.want {
				t.Errorf("WellFormedServiceToken = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"simple", "model-router", true},
		{"single letter", "a", true},
		{"single digit", "7", true},
		{"dots and underscores", "a.b_c-d", true},
		{"starts with digit", "1router", true},
		{"64 characters", strings.Repeat("a", 64), true},
		{"empty", "", false},
		{"65 characters", strings.Repeat("a", 65), false},
		{"upper case", "Router", false},
		{"upper case later", "routeR", false},
		{"starts with dash", "-router", false},
		{"starts with dot", ".router", false},
		{"starts with underscore", "_router", false},
		{"at sign", "router@x", false},
		{"space", "model router", false},
		{"slash", "model/router", false},
		{"non-ASCII", "rüter", false},
		{"newline", "router\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServiceName(tt.input)
			if (err == nil) != tt.ok {
				t.Errorf("ValidateServiceName(%q) = %v, want ok=%v", tt.input, err, tt.ok)
			}
		})
	}
}

func TestServiceUsernameAndRealm(t *testing.T) {
	if got := ServiceUsername("model-router"); got != "model-router@service.gowe" {
		t.Errorf("ServiceUsername = %q", got)
	}
	for _, u := range []string{"model-router@service.gowe", "X@SERVICE.GOWE", " x@service.gowe "} {
		if !IsServiceUsername(u) {
			t.Errorf("IsServiceUsername(%q) = false, want true", u)
		}
	}
	for _, u := range []string{"alice@bvbrc", "service.gowe", "x@service.gowe.example", "x@notservice.gowe", ""} {
		if IsServiceUsername(u) {
			t.Errorf("IsServiceUsername(%q) = true, want false", u)
		}
	}
}

// A user in the service realm is never an admin, whatever its stored role.
func TestUserIsAdmin_ServiceRealmNever(t *testing.T) {
	if (&User{Username: "model-router@service.gowe", Role: RoleAdmin}).IsAdmin() {
		t.Error("a service identity with role admin is reported admin")
	}
	if !(&User{Username: "alice", Role: RoleAdmin}).IsAdmin() {
		t.Error("an ordinary admin is not reported admin")
	}
}

func TestServiceTokenExpired(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Second), now.Add(time.Hour)
	if (&ServiceToken{}).Expired(now) {
		t.Error("a token without expiry is expired")
	}
	if !(&ServiceToken{ExpiresAt: &past}).Expired(now) {
		t.Error("a token past its expiry is not expired")
	}
	if !(&ServiceToken{ExpiresAt: &now}).Expired(now) {
		t.Error("a token at its expiry is not expired")
	}
	if (&ServiceToken{ExpiresAt: &future}).Expired(now) {
		t.Error("a token before its expiry is expired")
	}
	// An invalid stored expiry is expired, with or without ExpiresAt.
	if !(&ServiceToken{ExpiryInvalid: true}).Expired(now) {
		t.Error("a token with an invalid expiry is not expired")
	}
	if !(&ServiceToken{ExpiryInvalid: true, ExpiresAt: &future}).Expired(now) {
		t.Error("a token with an invalid expiry and a future ExpiresAt is not expired")
	}
}
