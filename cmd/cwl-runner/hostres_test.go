package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/me/gowe/internal/cwlrunner"
	"github.com/spf13/cobra"
)

// flagCmd registers the worker-parity flags the same way main() does, on a
// throwaway command, so parsing is tested end to end.
func flagCmd() *cobra.Command {
	preStageDir, envFile = "", ""
	datasetAliases, envVars, extraBindPaths = nil, nil, nil
	cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	registerHostResourceFlags(cmd)
	return cmd
}

func TestHostResourceFlags(t *testing.T) {
	pre := t.TempDir()
	if err := os.Mkdir(filepath.Join(pre, "boltz"), 0o755); err != nil {
		t.Fatal(err)
	}
	extraA, extraB := t.TempDir(), t.TempDir()
	envPath := filepath.Join(t.TempDir(), "w.env")
	if err := os.WriteFile(envPath, []byte("# c\n\nFROM_FILE=1\nBOTH=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		args      []string
		wantBinds []string
		wantEnv   map[string]string
		wantErr   bool
	}{
		{"none", nil, nil, nil, false},
		{"pre-stage-dir", []string{"--pre-stage-dir", pre}, []string{pre}, nil, false},
		{"extra-bind repeat and comma", []string{"--extra-bind", extraA, "--extra-bind", extraB + "," + extraA}, []string{extraA, extraB, extraA}, nil, false},
		{"dataset binds its path", []string{"--dataset", "b=/y,a=/x"}, []string{"/x", "/y"}, nil, false},
		{"dataset under pre-stage-dir not re-bound", []string{"--pre-stage-dir", pre, "--dataset", "w=" + filepath.Join(pre, "boltz")}, []string{pre}, nil, false},
		{"env repeat", []string{"--env", "FOO=bar", "--env", "A=b"}, nil, map[string]string{"FOO": "bar", "A": "b"}, false},
		{"env-file with comments", []string{"--env-file", envPath}, nil, map[string]string{"FROM_FILE": "1", "BOTH": "file"}, false},
		{"env overrides env-file", []string{"--env-file", envPath, "--env", "BOTH=flag"}, nil, map[string]string{"FROM_FILE": "1", "BOTH": "flag"}, false},
		{"reserved HOME", []string{"--env", "HOME=/x"}, nil, nil, true},
		{"reserved TMPDIR", []string{"--env", "TMPDIR=/x"}, nil, nil, true},
		{"bad env", []string{"--env", "NOEQUALS"}, nil, nil, true},
		{"missing pre-stage-dir", []string{"--pre-stage-dir", filepath.Join(pre, "nope")}, nil, nil, true},
		{"missing env-file", []string{"--env-file", filepath.Join(pre, "nope")}, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			cmd := flagCmd()
			err := cmd.ParseFlags(tt.args)
			var r *cwlrunner.Runner
			if err == nil {
				r = cwlrunner.NewRunner(logger)
				err = applyHostResources(r, logger)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			var gotBinds []string
			for _, b := range r.ExtraBinds {
				if b.HostPath != b.ContainerPath {
					t.Errorf("bind %+v not same-path", b)
				}
				gotBinds = append(gotBinds, b.HostPath)
			}
			if !reflect.DeepEqual(gotBinds, tt.wantBinds) {
				t.Errorf("binds = %v want %v", gotBinds, tt.wantBinds)
			}
			if !reflect.DeepEqual(r.EnvVars, tt.wantEnv) {
				t.Errorf("env = %v want %v", r.EnvVars, tt.wantEnv)
			}
		})
	}
}
