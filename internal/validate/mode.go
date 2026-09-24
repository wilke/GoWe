package validate

import "fmt"

// Mode controls what happens when submitted or tool-level input values do not
// match their declared CWL types (#273).
type Mode string

const (
	// ModeWarn accepts the value, logs it, and reports a warning. It is the
	// default, and the meaning of an empty Mode: tasks created by an older
	// server or before an upgrade carry no mode and must never start failing.
	ModeWarn Mode = "warn"
	// ModeEnforce rejects the submission (HTTP 400) or fails the task once,
	// without retries.
	ModeEnforce Mode = "enforce"
	// ModeOff skips the new type checks entirely. Existing required/null
	// checks still apply.
	ModeOff Mode = "off"
)

// ParseMode parses a --input-validation flag or GOWE_INPUT_VALIDATION value.
// The empty string means ModeWarn.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "", ModeWarn:
		return ModeWarn, nil
	case ModeEnforce, ModeOff:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("invalid input validation mode %q (want warn, enforce or off)", s)
	}
}

// Effective returns the mode to apply, mapping the empty mode to ModeWarn.
func (m Mode) Effective() Mode {
	if m == "" {
		return ModeWarn
	}
	return m
}
