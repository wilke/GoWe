// Package tokencrypt provides authenticated envelope encryption for short
// secrets — specifically the provider (BV-BRC / MG-RAST) tokens GoWe must
// persist so it can later run delegated jobs under the submitter's identity.
//
// Values are encrypted with AES-256-GCM under a single server-held key and
// stored as a self-describing string:
//
//	enc:v2:<base64(nonce || ciphertext || tag)>   // current: AAD-bound
//	enc:v1:<base64(nonce || ciphertext || tag)>   // legacy: no AAD
//
// New writes use v2, which binds the ciphertext to a caller-supplied context
// string (the row/column it lives in) as AES-GCM Additional Authenticated Data.
// A v2 blob only decrypts under the same context, so an attacker with DB-write
// access cannot relocate one row's ciphertext into another row and have it
// decrypt as that row's token. v1 blobs (written before AAD binding) still
// decrypt for backward compatibility, using no AAD.
//
// The marker prefix also lets readers distinguish ciphertext from legacy
// plaintext rows written before encryption was enabled, so an operator can turn
// the key on without a hard cutover: unmarked values pass through unchanged
// until they are re-encrypted.
package tokencrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// EnvKeyVar is the environment variable that carries the encryption key,
// encoded as base64 (standard or raw) or hex and decoding to exactly 32 bytes.
const EnvKeyVar = "GOWE_TOKEN_KEY"

// Marker prefixes for the on-disk encrypted formats. The version segment lets
// the format evolve without ambiguity. markerV2 binds AAD; markerV1 does not.
const (
	markerV1 = "enc:v1:"
	markerV2 = "enc:v2:"
)

// keyLen is the AES-256 key length in bytes.
const keyLen = 32

// Cipher encrypts and decrypts short secrets with AES-256-GCM. It is safe for
// concurrent use. The zero value is not usable; construct one with New.
type Cipher struct {
	aead cipher.AEAD
}

// New constructs a Cipher from a 32-byte key (AES-256).
func New(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("tokencrypt: key must be %d bytes for AES-256, got %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("tokencrypt: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("tokencrypt: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// DecodeKey decodes a key that is base64 (standard or raw-standard) or hex and
// must yield exactly 32 bytes. It is used by config/env loaders.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("tokencrypt: empty key")
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == keyLen {
		return b, nil
	}
	return nil, fmt.Errorf("tokencrypt: key must decode (base64 or hex) to %d bytes", keyLen)
}

// FromEnv builds a Cipher from EnvKeyVar. It returns (nil, nil) when the
// variable is unset or blank so callers can treat encryption as optional; a
// set-but-invalid key is a hard error (fail loudly on misconfiguration).
func FromEnv() (*Cipher, error) {
	raw := os.Getenv(EnvKeyVar)
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	key, err := DecodeKey(raw)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// IsEncrypted reports whether v was produced by Encrypt (has a v1 or v2 marker).
func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, markerV2) || strings.HasPrefix(v, markerV1)
}

// NeedsAADUpgrade reports whether v is an encrypted value not yet bound to a
// context (a legacy v1 blob). The migration uses this to re-encrypt v1 rows into
// the AAD-bound v2 format.
func NeedsAADUpgrade(v string) bool {
	return strings.HasPrefix(v, markerV1)
}

// Encrypt returns a v2 marked, authenticated ciphertext for plaintext, binding
// aad as Additional Authenticated Data so the value only decrypts under the same
// context. Empty input returns empty output (there is nothing to protect). Each
// call draws a fresh random nonce, so encrypting the same token twice yields
// different ciphertexts.
func (c *Cipher) Encrypt(plaintext, aad string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("tokencrypt: read nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), aadBytes(aad))
	return markerV2 + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. A v2 value is authenticated against aad; a legacy v1
// value is decrypted with no AAD (aad is ignored). A value without a marker is
// treated as legacy plaintext and returned unchanged, so rows written before
// encryption was enabled keep working until they are re-encrypted. Empty input
// returns empty. A marked-but-corrupt, wrong-key, or wrong-context value returns
// an error.
func (c *Cipher) Decrypt(v, aad string) (string, error) {
	if v == "" {
		return "", nil
	}
	switch {
	case strings.HasPrefix(v, markerV2):
		return c.open(strings.TrimPrefix(v, markerV2), aadBytes(aad))
	case strings.HasPrefix(v, markerV1):
		return c.open(strings.TrimPrefix(v, markerV1), nil)
	default:
		return v, nil // legacy plaintext passthrough
	}
}

// open decodes and authenticates a base64 nonce||ciphertext||tag blob.
func (c *Cipher) open(b64 string, aad []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("tokencrypt: decode ciphertext: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("tokencrypt: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return "", fmt.Errorf("tokencrypt: authenticate/decrypt: %w", err)
	}
	return string(pt), nil
}

// aadBytes converts a context string to AAD bytes; an empty context yields nil
// so it is interchangeable with "no AAD".
func aadBytes(aad string) []byte {
	if aad == "" {
		return nil
	}
	return []byte(aad)
}
