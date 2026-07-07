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
	for _, pt := range []string{"a", "bvbrc|token|expiry=123|sig=abc", strings.Repeat("x", 4096)} {
		enc, err := c.Encrypt(pt)
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if !IsEncrypted(enc) {
			t.Fatalf("ciphertext missing marker: %q", enc)
		}
		if strings.Contains(enc, pt) {
			t.Fatalf("ciphertext leaks plaintext: %q", enc)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if got != pt {
			t.Fatalf("round-trip mismatch: got %q want %q", got, pt)
		}
	}
}

func TestEmptyIsNoop(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt("")
	if err != nil || enc != "" {
		t.Fatalf("Encrypt(\"\") = %q, %v; want \"\", nil", enc, err)
	}
	dec, err := c.Decrypt("")
	if err != nil || dec != "" {
		t.Fatalf("Decrypt(\"\") = %q, %v; want \"\", nil", dec, err)
	}
}

func TestNonceIsRandomised(t *testing.T) {
	c := testCipher(t)
	a, _ := c.Encrypt("same-secret")
	b, _ := c.Encrypt("same-secret")
	if a == b {
		t.Fatalf("expected distinct ciphertexts for repeated Encrypt, got identical: %q", a)
	}
}

func TestDecryptLegacyPlaintextPassthrough(t *testing.T) {
	c := testCipher(t)
	// A value without the marker is a pre-encryption row; return it unchanged.
	got, err := c.Decrypt("legacy-plain-token")
	if err != nil {
		t.Fatalf("Decrypt(plaintext): %v", err)
	}
	if got != "legacy-plain-token" {
		t.Fatalf("passthrough mismatch: got %q", got)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	c := testCipher(t)
	enc, _ := c.Encrypt("secret")

	other, err := New([]byte("0123456789abcdef0123456789abcdef")) // 32 bytes, different key
	if err != nil {
		t.Fatalf("New other: %v", err)
	}
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("Decrypt with wrong key: expected error, got nil")
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	c := testCipher(t)
	enc, _ := c.Encrypt("secret")
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, marker))
	raw[len(raw)-1] ^= 0xFF // flip a tag bit
	tampered := marker + base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); err == nil {
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
