package worker

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/me/gowe/internal/validate"
)

// TestNewRedactedError_KeepsOutputValidationSentinel: redaction must not
// downgrade an output-validation failure to a retryable one.
func TestNewRedactedError_KeepsOutputValidationSentinel(t *testing.T) {
	base := fmt.Errorf("collect outputs: %w: output %q: secret-%s", validate.ErrOutputValidation, "o", testToken)
	err := newRedactedError(base, map[string]string{"t": testToken})
	if !errors.Is(err, validate.ErrOutputValidation) || !validate.IsPermanent(err) {
		t.Fatalf("sentinel lost: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked: %v", err)
	}
}
