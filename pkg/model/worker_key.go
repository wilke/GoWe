package model

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// WorkerKey is a per-worker authentication credential.
//
// Unlike the legacy shared worker key, each WorkerKey is individually
// attributable (via ID/Label), independently revocable (Disabled or deleted),
// and MAY carry an expiry. The raw secret is shown to the operator exactly once
// at issuance; only its SHA-256 hash (KeyHash) is persisted, so the datastore
// never holds the raw credential ("hashed at rest").
type WorkerKey struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	KeyHash     string     `json:"-"`          // sha256 hex of the raw key; never serialized
	KeyPrefix   string     `json:"key_prefix"` // non-secret leading fragment, for attribution
	Groups      []string   `json:"groups"`
	Description string     `json:"description,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreatedBy   string     `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// workerKeyRawPrefix marks raw worker-key secrets minted by GoWe ("GoWe Worker Key").
const workerKeyRawPrefix = "gwk_"

// workerKeyPrefixLen is how many leading characters of a raw key are retained as
// the non-secret display prefix. The raw key carries 256 bits of entropy, so a
// short prefix is safe to store and show for attribution.
const workerKeyPrefixLen = 12

// HashWorkerKey returns the hex-encoded SHA-256 of a raw worker key. This is the
// canonical at-rest representation; raw keys themselves are never stored.
func HashWorkerKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// WorkerKeyDisplayPrefix returns a short, non-secret leading fragment of a raw
// key, suitable for display and log attribution.
func WorkerKeyDisplayPrefix(raw string) string {
	if len(raw) <= workerKeyPrefixLen {
		return raw
	}
	return raw[:workerKeyPrefixLen]
}

// GenerateWorkerKey mints a new random worker key. It returns the raw secret
// (to be shown to the operator exactly once) together with its SHA-256 hash and
// display prefix. Callers persist only the hash and prefix.
func GenerateWorkerKey() (raw, hash, prefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate worker key: %w", err)
	}
	raw = workerKeyRawPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashWorkerKey(raw), WorkerKeyDisplayPrefix(raw), nil
}

// MatchesRaw reports whether raw hashes to this key's stored hash, using a
// constant-time comparison to avoid leaking information via timing.
func (k *WorkerKey) MatchesRaw(raw string) bool {
	return subtle.ConstantTimeCompare([]byte(k.KeyHash), []byte(HashWorkerKey(raw))) == 1
}

// IsActive reports whether the key may authenticate at time now: it must not be
// disabled and must not be past its expiry (if one is set).
func (k *WorkerKey) IsActive(now time.Time) bool {
	if k.Disabled {
		return false
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return false
	}
	return true
}
