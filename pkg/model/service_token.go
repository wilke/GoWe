package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ServiceToken is a credential GoWe itself issues to a program (for example
// the model router) so that it has an identity of its own. A request
// authenticated with a service token of name N is the user "N@service.gowe"
// (ServiceUsername), role user, never an admin, with no provider token.
//
// The raw token is returned once, when it is created; only its SHA-256 hash
// (TokenHash) is stored.
type ServiceToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	TokenHash   string     `json:"-"`            // sha256 hex of the raw token; never serialized
	TokenPrefix string     `json:"token_prefix"` // first 16 characters of the raw token, for display
	Description string     `json:"description"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`   // nil = no expiry
	LastUsedAt  *time.Time `json:"last_used_at"` // nil = never used
	// ExpiryInvalid is set by the store when the stored expiry is present
	// but empty or unparsable. Such a token is expired (Expired), never one
	// without expiry; ExpiresAt then points to the zero time.
	ExpiryInvalid bool `json:"-"`
}

// ServiceBVBRCDenied is the reason a service identity is refused the BV-BRC
// executor (at submission, at dispatch and on retry): it has no BV-BRC token,
// and the server's own BV-BRC account is never used for it.
const ServiceBVBRCDenied = "service identities cannot use the BV-BRC executor"

// BVBRCTokenRequired is the reason a submission without the submitter's
// provider token (anonymous, or a service identity) is refused the BV-BRC
// executor: at submission and retry (403) and, permanently, at dispatch.
const BVBRCTokenRequired = "the BV-BRC executor needs the submitter's BV-BRC token; this submission carries none (anonymous or service identity)"

const (
	// ServiceRealm is the realm of service identities. Usernames ending in
	// "@service.gowe" are reserved: they can only be reached with a service
	// token.
	ServiceRealm = "service.gowe"

	// ServiceTokenRawPrefix starts every raw service token.
	ServiceTokenRawPrefix = "gowe_svc_"

	// serviceTokenSecretBytes is the number of random bytes of a token.
	serviceTokenSecretBytes = 32
	// ServiceTokenLen is the length of a raw service token: the prefix and
	// 43 characters of unpadded base64url of 32 random bytes.
	ServiceTokenLen = len(ServiceTokenRawPrefix) + 43
	// ServiceTokenDisplayPrefixLen is how many leading characters of a raw
	// token are stored (and shown) as its token_prefix.
	ServiceTokenDisplayPrefixLen = 16

	// MaxServiceNameLen is the maximum length of a service name.
	MaxServiceNameLen = 64
	// MaxServiceTokenDescriptionBytes bounds the description of a token.
	MaxServiceTokenDescriptionBytes = 1000
	// MaxServiceTokensPerName is the maximum number of tokens of one name.
	MaxServiceTokensPerName = 20
	// MaxServiceTokens is the maximum number of service tokens in total.
	MaxServiceTokens = 500
)

// ServiceUsername returns the identity of a service token of the given name.
func ServiceUsername(name string) string {
	return name + "@" + ServiceRealm
}

// IsServiceUsername reports whether username is in the reserved service
// realm (case-insensitive, surrounding white space ignored). Such a username
// is never an admin, and a provider token claiming one is refused.
func IsServiceUsername(username string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(username)), "@"+ServiceRealm)
}

// Username returns the identity of requests authenticated with t.
func (t *ServiceToken) Username() string {
	return ServiceUsername(t.Name)
}

// Expired reports whether t has an expiry that is not after now, or an
// invalid stored expiry (ExpiryInvalid).
func (t *ServiceToken) Expired(now time.Time) bool {
	return t.ExpiryInvalid || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt))
}

// LooksLikeServiceToken reports whether a credential carries the service
// token prefix anywhere. Such a credential is handled only as a service
// token, never as a provider token.
func LooksLikeServiceToken(raw string) bool {
	return strings.Contains(raw, ServiceTokenRawPrefix)
}

// WellFormedServiceToken reports whether raw has the exact form of a service
// token: the prefix followed by 43 base64url characters.
func WellFormedServiceToken(raw string) bool {
	if len(raw) != ServiceTokenLen || !strings.HasPrefix(raw, ServiceTokenRawPrefix) {
		return false
	}
	for i := len(ServiceTokenRawPrefix); i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// HashServiceToken returns the lower-case hex SHA-256 of a raw service token,
// the only form in which a token is stored.
func HashServiceToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// GenerateServiceToken mints a new random service token. It returns the raw
// token (to be returned to the caller exactly once) with its hash and display
// prefix; callers persist only the hash and the prefix.
func GenerateServiceToken() (raw, hash, prefix string, err error) {
	buf := make([]byte, serviceTokenSecretBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate service token: %w", err)
	}
	raw = ServiceTokenRawPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashServiceToken(raw), raw[:ServiceTokenDisplayPrefixLen], nil
}

// ValidateServiceName checks a service name: 1-64 characters, a lower-case
// letter or digit, then lower-case letters, digits, '.', '_' or '-'.
func ValidateServiceName(name string) error {
	if name == "" {
		return errors.New("service name is empty")
	}
	if len(name) > MaxServiceNameLen {
		return fmt.Errorf("service name is %d characters long; at most %d are allowed", len(name), MaxServiceNameLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return fmt.Errorf("service name %q is invalid: it must start with a lower-case letter or digit, followed by lower-case letters, digits, '.', '_' or '-'", name)
		}
	}
	return nil
}
