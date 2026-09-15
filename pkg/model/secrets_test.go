package model

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSecretName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"HF_TOKEN", false},
		{"A", false},
		{"A1", false},
		{"REGISTRY_DEV_DSN", false},
		{"", true},
		{"lower_case", true},
		{"1LEADING_DIGIT", true},
		{"HAS-DASH", true},
		{"HAS SPACE", true},
		{"has.dot", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecretName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSecretName(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestValidateSecrets(t *testing.T) {
	t.Run("nil map is valid", func(t *testing.T) {
		if err := ValidateSecrets(nil); err != nil {
			t.Errorf("ValidateSecrets(nil) = %v, want nil", err)
		}
	})

	t.Run("valid map", func(t *testing.T) {
		m := map[string]string{"HF_TOKEN": "abc123", "API_KEY": "xyz"}
		if err := ValidateSecrets(m); err != nil {
			t.Errorf("ValidateSecrets(valid) = %v, want nil", err)
		}
	})

	t.Run("invalid name", func(t *testing.T) {
		m := map[string]string{"bad-name": "value"}
		if err := ValidateSecrets(m); err == nil {
			t.Error("expected error for invalid name")
		}
	})

	t.Run("empty value", func(t *testing.T) {
		m := map[string]string{"HF_TOKEN": ""}
		if err := ValidateSecrets(m); err == nil {
			t.Error("expected error for empty value")
		}
	})

	t.Run("oversized value", func(t *testing.T) {
		m := map[string]string{"HF_TOKEN": strings.Repeat("x", MaxSecretValueBytes+1)}
		if err := ValidateSecrets(m); err == nil {
			t.Error("expected error for oversized value")
		}
	})

	t.Run("too many entries", func(t *testing.T) {
		m := make(map[string]string, MaxSecretCount+1)
		for i := 0; i <= MaxSecretCount; i++ {
			m[secretNameForIndex(i)] = "v"
		}
		if err := ValidateSecrets(m); err == nil {
			t.Error("expected error for too many secrets")
		}
	})

	t.Run("at max entries is fine", func(t *testing.T) {
		m := make(map[string]string, MaxSecretCount)
		for i := 0; i < MaxSecretCount; i++ {
			m[secretNameForIndex(i)] = "v"
		}
		if err := ValidateSecrets(m); err != nil {
			t.Errorf("ValidateSecrets(%d entries) = %v, want nil", MaxSecretCount, err)
		}
	})
}

// secretNameForIndex generates a distinct, valid secret name for table-driven
// bulk tests (SECRET_0, SECRET_1, ...).
func secretNameForIndex(i int) string {
	return "SECRET_" + string(rune('A'+i%26)) + string(rune('0'+i/26))
}

func TestParseSecretsRetentionRoundTrip(t *testing.T) {
	tests := []struct {
		in       string
		wantKind SecretsRetentionKind
		wantTTL  time.Duration
		wantStr  string
	}{
		{"keep", SecretsRetentionKeep, 0, "keep"},
		{"", SecretsRetentionKeep, 0, "keep"},
		{"on_success", SecretsRetentionOnSuccess, 0, "on_success"},
		{"on_terminal", SecretsRetentionOnTerminal, 0, "on_terminal"},
		{"ttl:720h", SecretsRetentionTTL, 720 * time.Hour, "ttl:720h0m0s"},
		{"ttl:1h30m", SecretsRetentionTTL, 90 * time.Minute, "ttl:1h30m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			p, err := ParseSecretsRetention(tt.in)
			if err != nil {
				t.Fatalf("ParseSecretsRetention(%q) error = %v", tt.in, err)
			}
			if p.Kind != tt.wantKind {
				t.Errorf("Kind = %v, want %v", p.Kind, tt.wantKind)
			}
			if p.TTL != tt.wantTTL {
				t.Errorf("TTL = %v, want %v", p.TTL, tt.wantTTL)
			}
			if got := p.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
			// Round-trip: String() must parse back to an equal policy.
			p2, err := ParseSecretsRetention(p.String())
			if err != nil {
				t.Fatalf("re-parse %q: %v", p.String(), err)
			}
			if p2 != p {
				t.Errorf("round-trip mismatch: got %+v, want %+v", p2, p)
			}
		})
	}
}

func TestParseSecretsRetentionErrors(t *testing.T) {
	tests := []string{
		"bogus",
		"ttl:",
		"ttl:notaduration",
		"ttl:-1h",
		"ttl:0h",
	}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseSecretsRetention(in); err == nil {
				t.Errorf("ParseSecretsRetention(%q): expected error, got nil", in)
			}
		})
	}
}

func TestSubmissionSecretsState(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		s := &Submission{}
		if got := s.SecretsState(); got != "none" {
			t.Errorf("SecretsState() = %q, want none", got)
		}
	})

	t.Run("present", func(t *testing.T) {
		s := &Submission{SecretNames: []string{"HF_TOKEN"}}
		if got := s.SecretsState(); got != "present" {
			t.Errorf("SecretsState() = %q, want present", got)
		}
	})

	t.Run("purged", func(t *testing.T) {
		now := time.Now()
		s := &Submission{SecretNames: []string{"HF_TOKEN"}, SecretsPurgedAt: &now}
		if got := s.SecretsState(); got != "purged" {
			t.Errorf("SecretsState() = %q, want purged", got)
		}
	})

	t.Run("purged wins even with empty names", func(t *testing.T) {
		now := time.Now()
		s := &Submission{SecretsPurgedAt: &now}
		if got := s.SecretsState(); got != "purged" {
			t.Errorf("SecretsState() = %q, want purged", got)
		}
	})
}
