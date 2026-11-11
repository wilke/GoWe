package model

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestWriteErrorDetails(t *testing.T) {
	twoDetails := NewValidationError("CWL validation failed",
		FieldError{Field: "outputs.y.outputSource", Message: `outputSource "s/z" does not match any step output or workflow input`},
		FieldError{Field: "inputs.x", Path: "[0]", Message: "bad"},
	)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"api error with details", twoDetails,
			"  outputs.y.outputSource: outputSource \"s/z\" does not match any step output or workflow input\n  inputs.x[0]: bad\n"},
		{"wrapped api error", fmt.Errorf("validate: %w", twoDetails),
			"  outputs.y.outputSource: outputSource \"s/z\" does not match any step output or workflow input\n  inputs.x[0]: bad\n"},
		{"api error without details", &APIError{Code: ErrNotFound, Message: "x"}, ""},
		{"plain error", errors.New("boom"), ""},
		{"nil error", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			WriteErrorDetails(&b, tt.err)
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}
