// Package tokencrypt provides authenticated envelope encryption for short
// secrets — specifically the provider (BV-BRC / MG-RAST) tokens GoWe must
// persist so it can later run delegated jobs under the submitter's identity.
//
// Values are encrypted with AES-256-GCM under a single server-held key and
// stored as a self-describing string:
//
//	enc:v1:<base64(nonce || ciphertext || tag)>
//
// The marker prefix lets readers distinguish ciphertext from legacy plaintext
// rows written before encryption was enabled, so an operator can turn the key
// on without a hard cutover: unmarked values pass through unchanged until they
// are re-encrypted.
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

// marker prefixes an encrypted value. The version segment allows the on-disk
// format to evolve without ambiguity.
const marker = "enc:v1:"

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

// IsEncrypted reports whether v was produced by Encrypt (has the marker prefix).
func IsEncrypted(v string) bool {
	return strings.HasPrefix(v, marker)
}

// Encrypt returns a marked, authenticated ciphertext for plaintext. Empty input
// returns empty output (there is nothing to protect). Each call draws a fresh
// random nonce, so encrypting the same token twice yields different ciphertexts.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("tokencrypt: read nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return marker + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. A value without the marker is treated as legacy
// plaintext and returned unchanged, so rows written before encryption was
// enabled keep working until they are re-encrypted. Empty input returns empty.
// A marked-but-corrupt or wrong-key value returns an error.
func (c *Cipher) Decrypt(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !IsEncrypted(v) {
		return v, nil // legacy plaintext passthrough
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, marker))
	if err != nil {
		return "", fmt.Errorf("tokencrypt: decode ciphertext: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("tokencrypt: ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("tokencrypt: authenticate/decrypt: %w", err)
	}
	return string(pt), nil
}
