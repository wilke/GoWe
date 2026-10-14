package toolexec

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// maxStderrTail bounds the tool stderr appended to an output-collection error.
const maxStderrTail = 2048

// collectOutputsError wraps an output-collection/validation failure and
// appends the tail of the tool's stderr (secret-masked), which is otherwise
// lost with the discarded Result exactly when it explains the failure. The
// original error stays wrapped (errors.Is keeps working).
func collectOutputsError(err error, stderr string, secrets map[string]string) error {
	tail := stderrTail(stderr, secrets)
	if tail == "" {
		return fmt.Errorf("collect outputs: %w", err)
	}
	return fmt.Errorf("collect outputs: %w\ntool stderr (tail):\n%s", err, tail)
}

// stderrTail masks secrets in the whole of stderr first and only then keeps
// the last maxStderrTail bytes, cut on a UTF-8 rune boundary. Masking after
// cutting would let a secret that straddles the cut leave its unmasked end in
// the tail.
func stderrTail(stderr string, secrets map[string]string) string {
	s := strings.TrimSpace(MaskSecretValues([]string{stderr}, secrets)[0])
	if len(s) <= maxStderrTail {
		return s
	}
	cut := len(s) - maxStderrTail
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "..." + s[cut:]
}
