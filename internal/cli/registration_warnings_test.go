package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPrintRegistrationWarnings(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"two warnings", `{"id":"wf_1","warnings":["steps.s.in.treads: input \"treads\" is ignored","tool x is not blessed"]}`,
			"Warnings:\n  steps.s.in.treads: input \"treads\" is ignored\n  tool x is not blessed\n"},
		{"no warnings field", `{"id":"wf_1"}`, ""},
		{"empty list", `{"id":"wf_1","warnings":[]}`, ""},
		{"not json", `nope`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			printRegistrationWarnings(&b, json.RawMessage(tt.data))
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}
