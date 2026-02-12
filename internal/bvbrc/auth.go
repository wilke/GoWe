package bvbrc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TokenInfo contains parsed fields from a pipe-delimited BV-BRC token.
type TokenInfo struct {
	Raw      string
	Username string
	Expiry   time.Time
}

// ParseToken extracts username and expiry from a pipe-delimited BV-BRC token.
// Format: un=<user>|tokenid=<uuid>|expiry=<unix>|...
// Returns a zero-value TokenInfo for empty or malformed tokens.
func ParseToken(raw string) TokenInfo {
	info := TokenInfo{Raw: strings.TrimSpace(raw)}
	for _, field := range strings.Split(info.Raw, "|") {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "un":
			info.Username = v
		case "expiry":
			if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
				info.Expiry = time.Unix(ts, 0)
			}
		}
	}
	return info
}

// IsExpired reports whether the token's expiry time has passed.
// A zero expiry (unparsed or absent) is treated as not expired.
func (t TokenInfo) IsExpired() bool {
	if t.Expiry.IsZero() {
		return false
	}
	return time.Now().After(t.Expiry)
}

// ResolveToken loads a BV-BRC token from the first available source:
//  1. BVBRC_TOKEN environment variable
//  2. ~/.gowe/credentials.json (same format as "gowe login")
//  3. ~/.bvbrc_token file
//  4. ~/.patric_token file
//  5. ~/.p3_token file
func ResolveToken() (string, error) {
	if tok := os.Getenv("BVBRC_TOKEN"); tok != "" {
		return strings.TrimSpace(tok), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve token: %w", err)
	}

	// Try ~/.gowe/credentials.json (same shape as internal/cli/login.go).
	if tok, err := readCredentialsJSON(filepath.Join(home, ".gowe", "credentials.json")); err == nil && tok != "" {
		return tok, nil
	}

	// Try well-known token files.
	for _, name := range []string{".bvbrc_token", ".patric_token", ".p3_token"} {
		if tok, err := readTokenFile(filepath.Join(home, name)); err == nil && tok != "" {
			return tok, nil
		}
	}

	return "", fmt.Errorf("no BV-BRC token found (set BVBRC_TOKEN or run gowe login)")
}

// readCredentialsJSON reads a token from a JSON file with shape {"token":"..."}.
func readCredentialsJSON(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var creds struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", err
	}
	return strings.TrimSpace(creds.Token), nil
}

// readTokenFile reads a plain-text token from a file, trimming whitespace.
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
