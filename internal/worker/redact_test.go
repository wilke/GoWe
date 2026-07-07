package worker

import "testing"

// TestRedactSecrets verifies injected credential values are scrubbed from
// captured output before it leaves the worker (SPECIFICATION.md §13.2).
func TestRedactSecrets(t *testing.T) {
	secrets := map[string]string{
		"BVBRC_TOKEN":   "un=clark|si=abc|token=SECRETVALUE123456",
		"KB_AUTH_TOKEN": "un=clark|si=abc|token=SECRETVALUE123456",
		"SHORT":         "x", // < 6 bytes: skipped to avoid over-redaction
	}

	out := redactSecrets("starting\nenv: BVBRC_TOKEN=un=clark|si=abc|token=SECRETVALUE123456\ndone x", secrets)
	if contains(out, "SECRETVALUE123456") {
		t.Errorf("token value leaked through redaction: %q", out)
	}
	if !contains(out, "***REDACTED***") {
		t.Errorf("expected redaction marker, got %q", out)
	}
	// The short secret "x" must NOT trigger redaction of ordinary text.
	if !contains(out, "done x") {
		t.Errorf("short secret over-redacted normal text: %q", out)
	}

	// No secrets / empty input are pass-through.
	if got := redactSecrets("hello", nil); got != "hello" {
		t.Errorf("no-secrets passthrough: got %q", got)
	}
	if got := redactSecrets("", secrets); got != "" {
		t.Errorf("empty input: got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
