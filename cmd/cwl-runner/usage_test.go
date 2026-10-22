package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestSilenceUsageOnRunErrors(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantUsage bool
	}{
		{"run error has no usage", []string{"tool.cwl"}, false},
		{"subcommand run error has no usage", []string{"sub", "tool.cwl"}, false},
		{"missing argument keeps usage", nil, true},
		{"unknown flag keeps usage", []string{"--nope", "tool.cwl"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail := func(*cobra.Command, []string) error { return errors.New("command failed: exit status 3") }
			root := &cobra.Command{Use: "cwl-runner", Args: cobra.MinimumNArgs(1), RunE: fail}
			root.AddCommand(&cobra.Command{Use: "sub", Args: cobra.ExactArgs(1), RunE: fail})
			silenceUsageOnRunErrors(root)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tt.args)
			if err := root.Execute(); err == nil {
				t.Fatal("expected an error")
			}
			if got := strings.Contains(out.String(), "Usage:"); got != tt.wantUsage {
				t.Errorf("usage printed = %v, want %v; output:\n%s", got, tt.wantUsage, out.String())
			}
		})
	}
}
