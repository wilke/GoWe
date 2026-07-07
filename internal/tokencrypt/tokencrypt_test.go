package tokencrypt

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, keyLen)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33, 64} {
		if _, err := New(make([]byte, n)); err == nil {
			t.Errorf("New(%d-byte key): expected error, got nil", n)
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t)
	const aad = "submission.user_token:sub_123"
	for _, pt := range []string{"a", "bvbrc|token|expiry=123|sig=abc", strings.Repeat("x", 4096)} {
		enc, err := c.Encrypt(pt, aad)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if !IsEncrypted(enc) {
			t.Fatalf("ciphertext missing marker: %q", enc)
		}
		if !strings.HasPrefix(enc, markerV2) {
			t.Fatalf("new writes must use v2 format: %q", enc)
		}
		// The leak check is only meaningful for plaintexts long enough that a
		// coincidental substring in the base64 ciphertext is negligible. A
		// single character like "a" appears in a random ~44-char base64 string
		// roughly half the time, which made this assertion flaky (~50%).
		if len(pt) >= 8 && strings.Contains(enc, pt) {
			t.Fatalf("ciphertext leaks plaintext: %q", enc)
		}
		got, err := c.Decrypt(enc, aad)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != pt {
			t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

// TestAADBindingRejectsWrongContext verifies a v2 ciphertext only decrypts under
// the exact context it was sealed with — so a blob moved to another row (a
// different AAD) fails authentication rather than decrypting as that row's token.
func TestAADBindingRejectsWrongContext(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt("secret-token", "submission.user_token:sub_A")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Same context: succeeds.
	if got, err := c.Decrypt(enc, "submission.user_token:sub_A"); err != nil || got != "secret-token" {
		t.Fatalf("Decrypt with matching AAD: got (%q, %v)", got, err)
	}
	// Different row's context: must fail.
	if _, err := c.Decrypt(enc, "submission.user_token:sub_B"); err == nil {
		t.Fatal("Decrypt with wrong AAD context: expected error, got nil (ciphertext relocatable)")
	}
	// Empty context (as a v1 reader would use): must also fail for a v2 blob.
	if _, err := c.Decrypt(enc, ""); err == nil {
		t.Fatal("Decrypt of v2 blob with empty AAD: expected error, got nil")
	}
}

// TestDecryptLegacyV1NoAAD verifies that ciphertext written in the pre-AAD v1
// format still decrypts (using no AAD), so existing rows keep working.
func TestDecryptLegacyV1NoAAD(t *testing.T) {
	c := testCipher(t)
	// Build a v1 blob directly (the old format: nonce||ct||tag, no AAD).
	nonce := make([]byte, c.aead.NonceSize())
	sealed := c.aead.Seal(nonce, nonce, []byte("legacy-secret"), nil)
	v1 := markerV1 + base64.StdEncoding.EncodeToString(sealed)

	if !NeedsAADUpgrade(v1) {
		t.Error("NeedsAADUpgrade(v1) = false, want true")
	}
	// Decrypts regardless of the aad argument (v1 ignores it).
	for _, aad := range []string{"", "some.context:id"} {
		got, err := c.Decrypt(v1, aad)
		if err != nil {
			t.Fatalf("Decrypt(v1, %q): %v", aad, err)
		}
		if got != "legacy-secret" {
			t.Fatalf("v1 round-trip mismatch: got %q", got)
		}
	}
}

func TestEmptyIsNoop(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt("", "ctx")
	if err != nil || enc != "" {
		t.Fatalf("Encrypt(\"\") = %q, %v; want \"\", nil", enc, err)
	}
	dec, err := c.Decrypt("", "ctx")
	if err != nil || dec != "" {
		t.Fatalf("Decrypt(\"\") = %q, %v; want \"\", nil", dec, err)
	}
}

func TestNonceIsRandomised(t *testing.T) {
	c := testCipher(t)
	a, _ := c.Encrypt("same-secret", "ctx")
	b, _ := c.Encrypt("same-secret", "ctx")
	if a == b {
		t.Fatalf("expected distinct ciphertexts for repeated Encrypt, got identical: %q", a)
	}
}

func TestDecryptLegacyPlaintextPassthrough(t *testing.T) {
	c := testCipher(t)
	// A value without a marker is a pre-encryption row; return it unchanged.
	got, err := c.Decrypt("legacy-plain-token", "ctx")
	if err != nil {
		t.Fatalf("Decrypt(plaintext): %v", err)
	}
	if got != "legacy-plain-token" {
		t.Fatalf("passthrough mismatch: got %q", got)
	}
	if IsEncrypted("legacy-plain-token") {
		t.Error("IsEncrypted(plaintext) = true")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	c := testCipher(t)
	enc, _ := c.Encrypt("secret", "ctx")

	other, err := New([]byte("0123456789abcdef0123456789abcdef")) // 32 bytes, different key
	if err != nil {
		t.Fatalf("New other: %v", err)
	}
	if _, err := other.Decrypt(enc, "ctx"); err == nil {
		t.Fatal("Decrypt with wrong key: expected error, got nil")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	c := testCipher(t)
	enc, _ := c.Encrypt("secret", "ctx")
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, markerV2))
	raw[len(raw)-1] ^= 0xFF // flip a tag bit
	tampered := markerV2 + base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered, "ctx"); err == nil {
		t.Fatal("Decrypt of tampered ciphertext: expected error, got nil")
	}
}

func TestDecodeKey(t *testing.T) {
	key := make([]byte, keyLen)
	for i := range key {
		key[i] = byte(i)
	}
	cases := []string{
		base64.StdEncoding.EncodeToString(key),
		base64.RawStdEncoding.EncodeToString(key),
		hex.EncodeToString(key),
	}
	for _, enc := range cases {
		got, err := DecodeKey(enc)
		if err != nil {
			t.Fatalf("DecodeKey(%q): %v", enc, err)
		}
		if string(got) != string(key) {
			t.Fatalf("DecodeKey(%q) mismatch", enc)
		}
	}
	for _, bad := range []string{"", "short", hex.EncodeToString(make([]byte, 16))} {
		if _, err := DecodeKey(bad); err == nil {
			t.Errorf("DecodeKey(%q): expected error", bad)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvKeyVar, "")
	c, err := FromEnv()
	if err != nil || c != nil {
		t.Fatalf("FromEnv unset: got (%v, %v); want (nil, nil)", c, err)
	}

	key := make([]byte, keyLen)
	t.Setenv(EnvKeyVar, base64.StdEncoding.EncodeToString(key))
	c, err = FromEnv()
	if err != nil || c == nil {
		t.Fatalf("FromEnv valid: got (%v, %v); want cipher, nil", c, err)
	}

	t.Setenv(EnvKeyVar, "not-a-valid-key")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv invalid: expected error, got nil")
	}
}
