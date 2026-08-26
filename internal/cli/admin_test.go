package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateAdminVerifyArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		all     bool
		wantErr string
	}{
		{"single id", []string{"sub_1"}, false, ""},
		{"all only", nil, true, ""},
		{"no id no all", nil, false, "requires exactly one submission ID"},
		{"two ids", []string{"sub_1", "sub_2"}, false, "requires exactly one submission ID"},
		{"all with id", []string{"sub_1"}, true, "--all cannot be combined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAdminVerifyArgs(tt.args, tt.all)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestAdminCommands_ArgParsing drives the cobra tree: argument errors must
// surface before any HTTP request is made.
func TestAdminCommands_ArgParsing(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"verify without target", []string{"admin", "verify-outputs"}, "requires exactly one submission ID"},
		{"verify all with id", []string{"admin", "verify-outputs", "--all", "sub_1"}, "--all cannot be combined"},
		{"redeliver without id", []string{"admin", "redeliver"}, "accepts 1 arg"},
		{"redeliver two ids", []string{"admin", "redeliver", "a", "b"}, "accepts 1 arg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			// Point at a closed port so an accidental request fails fast
			// instead of hanging; arg validation must fire before it anyway.
			root.SetArgs(append([]string{"--server", "http://127.0.0.1:1"}, tt.args...))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestShortChecksum(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "-"},
		{"sha1$0123456789abcdef0123456789abcdef01234567", "sha1$0123456789ab"},
		{"sha1$abc", "sha1$abc"},
		{"nodollar", "nodollar"},
	}
	for _, tt := range tests {
		if got := shortChecksum(tt.in); got != tt.want {
			t.Errorf("shortChecksum(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
