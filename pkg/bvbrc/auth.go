package bvbrc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Token file locations in order of preference.
var tokenFiles = []string{
	".bvbrc_token",
	".patric_token",
	".p3_token",
}

// ParseToken parses a BV-BRC token string into its component fields.
// Token format: un=<username>|tokenid=<uuid>|expiry=<unix_timestamp>|client_id=<client_id>|token_type=Bearer|...|sig=<signature>
func ParseToken(raw string) (*AuthToken, error) {
	if raw == "" {
		return nil, ErrInvalidTokenFormat
	}

	token := &AuthToken{Raw: raw}

	parts := strings.Split(raw, "|")
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key, value := kv[0], kv[1]
		switch key {
		case "un":
			token.Username = value
		case "tokenid":
			token.TokenID = value
		case "expiry":
			ts, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid expiry timestamp", ErrInvalidTokenFormat)
			}
			token.Expiry = time.Unix(ts, 0)
		case "client_id":
			token.ClientID = value
		case "sig":
			token.Signature = value
		}
	}

	// Validate required fields
	if token.Username == "" {
		return nil, fmt.Errorf("%w: missing username", ErrInvalidTokenFormat)
	}
	if token.TokenID == "" {
		return nil, fmt.Errorf("%w: missing token ID", ErrInvalidTokenFormat)
	}
	if token.Expiry.IsZero() {
		return nil, fmt.Errorf("%w: missing expiry", ErrInvalidTokenFormat)
	}

	return token, nil
}

// LoadTokenFromEnv attempts to load a token from the BVBRC_TOKEN environment variable.
func LoadTokenFromEnv() (string, error) {
	token := os.Getenv("BVBRC_TOKEN")
	if token == "" {
		// Also try legacy environment variable
		token = os.Getenv("P3_AUTH_TOKEN")
	}
	if token == "" {
		return "", ErrNoTokenFile
	}
	return strings.TrimSpace(token), nil
}

// LoadTokenFromFile attempts to load a token from the standard file locations.
// It tries ~/.bvbrc_token, ~/.patric_token, and ~/.p3_token in order.
func LoadTokenFromFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	for _, name := range tokenFiles {
		path := filepath.Join(home, name)
		data, err := os.ReadFile(path)
		if err == nil {
			token := strings.TrimSpace(string(data))
			if token != "" {
				return token, nil
			}
		}
	}

	return "", ErrNoTokenFile
}

// LoadToken attempts to load a token from environment or file.
// Order of precedence:
// 1. BVBRC_TOKEN environment variable
// 2. P3_AUTH_TOKEN environment variable
// 3. ~/.bvbrc_token file
// 4. ~/.patric_token file
// 5. ~/.p3_token file
func LoadToken() (string, error) {
	// Try environment first
	token, err := LoadTokenFromEnv()
	if err == nil {
		return token, nil
	}

	// Fall back to file
	return LoadTokenFromFile()
}

// SaveToken writes a token to the standard BV-BRC token file (~/.bvbrc_token).
func SaveToken(token string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory: %w", err)
	}

	path := filepath.Join(home, ".bvbrc_token")
	return os.WriteFile(path, []byte(token), 0600)
}

// UsernameFromToken extracts the username from a raw token string.
// Returns empty string if the token cannot be parsed.
func UsernameFromToken(token string) string {
	parsed, err := ParseToken(token)
	if err != nil {
		return ""
	}
	return parsed.Username
}

// WorkspacePath constructs a workspace path for the given user.
// Format: /username@patricbrc.org/workspace/path
func WorkspacePath(username, workspace, path string) string {
	if workspace == "" {
		workspace = "home"
	}
	path = strings.TrimPrefix(path, "/")
	return fmt.Sprintf("/%s@patricbrc.org/%s/%s", username, workspace, path)
}
