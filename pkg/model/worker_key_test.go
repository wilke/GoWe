package model

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateWorkerKey(t *testing.T) {
	raw1, hash1, prefix1, err := GenerateWorkerKey()
	if err != nil {
		t.Fatalf("GenerateWorkerKey: %v", err)
	}
	if !strings.HasPrefix(raw1, "gwk_") {
		t.Errorf("raw key %q missing gwk_ prefix", raw1)
	}
	if hash1 != HashWorkerKey(raw1) {
		t.Errorf("returned hash does not match HashWorkerKey(raw)")
	}
	if prefix1 != raw1[:workerKeyPrefixLen] {
		t.Errorf("prefix %q not a leading fragment of raw", prefix1)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(hash1))
	}

	// Keys must be unique across calls.
	raw2, _, _, err := GenerateWorkerKey()
	if err != nil {
		t.Fatalf("GenerateWorkerKey: %v", err)
	}
	if raw1 == raw2 {
		t.Errorf("two generated keys collided: %q", raw1)
	}
}

func TestHashWorkerKeyDeterministic(t *testing.T) {
	h1 := HashWorkerKey("some-secret")
	h2 := HashWorkerKey("some-secret")
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q vs %q", h1, h2)
	}
	if HashWorkerKey("a") == HashWorkerKey("b") {
		t.Errorf("distinct inputs produced same hash")
	}
}

func TestWorkerKeyMatchesRaw(t *testing.T) {
	raw, hash, _, _ := GenerateWorkerKey()
	k := &WorkerKey{KeyHash: hash}
	if !k.MatchesRaw(raw) {
		t.Errorf("MatchesRaw(correct key) = false, want true")
	}
	if k.MatchesRaw(raw + "x") {
		t.Errorf("MatchesRaw(wrong key) = true, want false")
	}
}

func TestWorkerKeyIsActive(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		key  WorkerKey
		want bool
	}{
		{"enabled no expiry", WorkerKey{}, true},
		{"disabled", WorkerKey{Disabled: true}, false},
		{"not yet expired", WorkerKey{ExpiresAt: &future}, true},
		{"expired", WorkerKey{ExpiresAt: &past}, false},
		{"expires exactly now", WorkerKey{ExpiresAt: &now}, false},
		{"disabled and unexpired", WorkerKey{Disabled: true, ExpiresAt: &future}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.IsActive(now); got != tt.want {
				t.Errorf("IsActive() = %v, want %v", got, tt.want)
			}
		})
	}
}
