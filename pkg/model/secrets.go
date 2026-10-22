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

// MinSecretValueBytes is the minimum length of a submission-time secret
// value. Below this, the masking thresholds used at the execution boundary
// (toolexec/worker: values shorter than 8 bytes are never masked in logs or
// the container process table) would let a short secret through unmasked
// everywhere it is surfaced. Rejecting anything shorter than the masking
// threshold at validation time is simpler than lowering the threshold.
const MinSecretValueBytes = 8

// MaxSecretCount caps the number of secrets a single submission may carry.
const MaxSecretCount = 64

// ValidateSecrets validates a submission's secrets map: every name must
// satisfy ValidateSecretName, every value must be at least MinSecretValueBytes
// and no larger than MaxSecretValueBytes, and the map may carry at most
// MaxSecretCount entries. Returns nil for a nil/empty map. Error messages
// name only the secret NAME, never its value.
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
		if len(value) < MinSecretValueBytes {
			return fmt.Errorf("secret %q: value must be at least %d bytes", name, MinSecretValueBytes)
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
	// SecretsRetentionTTL purges secret values TTL after the submission
	// reaches a terminal state (measured from CompletedAt, falling back to
	// CreatedAt when CompletedAt is unset — see secretsPurgeDue).
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

// SecretNameForInput derives the Secrets key under which a workflow input
// declared secret via cwltool:Secrets is stored: "INPUT_" + the input id
// upper-cased with every character outside [A-Z0-9] replaced by '_'. The
// mapping is deterministic so the server (which strips the value at
// submission) and the worker (which re-injects it into the job) agree
// without exchanging anything but the input id.
func SecretNameForInput(inputID string) string {
	b := make([]byte, 0, len(inputID)+6)
	b = append(b, "INPUT_"...)
	for _, r := range strings.ToUpper(inputID) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b = append(b, byte(r))
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}

// SecretInputPlaceholder is the value persisted in inputs/submitted_inputs in
// place of a cwltool:Secrets-declared input; the real value lives in Secrets.
const SecretInputPlaceholder = "<secret>"
