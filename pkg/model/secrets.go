package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SecretNamePattern is the required shape of a submission-time secret name:
// an uppercase identifier suitable for direct use as an environment variable
// name (SCREAMING_SNAKE_CASE), matching the convention already used by
// worker --secret/--secret-file (see CLAUDE.md "Worker Secret Environment
// Variables").
const SecretNamePattern = "^[A-Z][A-Z0-9_]*$"

var secretNameRe = regexp.MustCompile(SecretNamePattern)

// ValidateSecretName returns an error unless n matches SecretNamePattern.
func ValidateSecretName(n string) error {
	if !secretNameRe.MatchString(n) {
		return fmt.Errorf("invalid secret name %q: must match %s", n, SecretNamePattern)
	}
	return nil
}

// MaxSecretValueBytes caps the size of a single secret value.
const MaxSecretValueBytes = 64 * 1024

// MaxSecretCount caps the number of secrets a single submission may carry.
const MaxSecretCount = 64

// ValidateSecrets validates a submission's secrets map: every name must
// satisfy ValidateSecretName, every value must be non-empty and no larger
// than MaxSecretValueBytes, and the map may carry at most MaxSecretCount
// entries. Returns nil for a nil/empty map.
func ValidateSecrets(m map[string]string) error {
	if len(m) == 0 {
		return nil
	}
	if len(m) > MaxSecretCount {
		return fmt.Errorf("too many secrets: %d (max %d)", len(m), MaxSecretCount)
	}
	for name, value := range m {
		if err := ValidateSecretName(name); err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("secret %q: value must not be empty", name)
		}
		if len(value) > MaxSecretValueBytes {
			return fmt.Errorf("secret %q: value exceeds %d bytes", name, MaxSecretValueBytes)
		}
	}
	return nil
}

// SecretsRetentionKind identifies the shape of a SecretsRetentionPolicy.
type SecretsRetentionKind int

const (
	// SecretsRetentionKeep never purges secret values automatically.
	SecretsRetentionKeep SecretsRetentionKind = iota
	// SecretsRetentionTTL purges secret values TTL after submission creation.
	SecretsRetentionTTL
	// SecretsRetentionOnSuccess purges secret values once the submission
	// reaches SubmissionStateCompleted.
	SecretsRetentionOnSuccess
	// SecretsRetentionOnTerminal purges secret values once the submission
	// reaches any terminal state (COMPLETED, FAILED, or CANCELLED).
	SecretsRetentionOnTerminal
)

// SecretsRetentionPolicy is a submission's secret-value retention policy.
// TTL is only meaningful when Kind is SecretsRetentionTTL.
type SecretsRetentionPolicy struct {
	Kind SecretsRetentionKind
	TTL  time.Duration
}

// String round-trips with ParseSecretsRetention: "keep", "ttl:<duration>",
// "on_success", or "on_terminal".
func (p SecretsRetentionPolicy) String() string {
	switch p.Kind {
	case SecretsRetentionTTL:
		return "ttl:" + p.TTL.String()
	case SecretsRetentionOnSuccess:
		return "on_success"
	case SecretsRetentionOnTerminal:
		return "on_terminal"
	default:
		return "keep"
	}
}

// ParseSecretsRetention parses a submission's secrets_retention string. An
// empty string is treated as "keep" (the default: no automatic purge).
func ParseSecretsRetention(s string) (SecretsRetentionPolicy, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "" || s == "keep":
		return SecretsRetentionPolicy{Kind: SecretsRetentionKeep}, nil
	case s == "on_success":
		return SecretsRetentionPolicy{Kind: SecretsRetentionOnSuccess}, nil
	case s == "on_terminal":
		return SecretsRetentionPolicy{Kind: SecretsRetentionOnTerminal}, nil
	case strings.HasPrefix(s, "ttl:"):
		ttlStr := strings.TrimPrefix(s, "ttl:")
		ttl, err := time.ParseDuration(ttlStr)
		if err != nil {
			return SecretsRetentionPolicy{}, fmt.Errorf("invalid secrets retention %q: %w", s, err)
		}
		if ttl <= 0 {
			return SecretsRetentionPolicy{}, fmt.Errorf("invalid secrets retention %q: TTL must be positive", s)
		}
		return SecretsRetentionPolicy{Kind: SecretsRetentionTTL, TTL: ttl}, nil
	default:
		return SecretsRetentionPolicy{}, fmt.Errorf("invalid secrets retention %q: must be \"keep\", \"ttl:<duration>\", \"on_success\", or \"on_terminal\"", s)
	}
}
