package toolexec

import (
	"fmt"
	"strings"
)

// collectOutputsError wraps an output-collection/validation failure and
// appends the tail of the tool's stderr (secret-masked), which is otherwise
// lost with the discarded Result exactly when it explains the failure. The
// original error stays wrapped (errors.Is keeps working).
func collectOutputsError(err error, stderr string, secrets map[string]string) error {
	const maxTail = 2048
	tail := strings.TrimSpace(stderr)
	if len(tail) > maxTail {
		tail = "..." + tail[len(tail)-maxTail:]
	}
	if tail == "" {
		return fmt.Errorf("collect outputs: %w", err)
	}
	tail = MaskSecretValues([]string{tail}, secrets)[0]
	return fmt.Errorf("collect outputs: %w\ntool stderr (tail):\n%s", err, tail)
}
