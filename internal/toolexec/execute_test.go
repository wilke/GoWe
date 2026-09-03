package toolexec

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

func TestResolveApptainerImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		imageDir string
		want     string
	}{
		{
			name:  "registry image gets docker prefix",
			image: "dxkb/esmfold:latest",
			want:  "docker://dxkb/esmfold:latest",
		},
		{
			name:  "registry image with no tag",
			image: "ubuntu",
			want:  "docker://ubuntu",
		},
		{
			name:  "absolute sif path used as-is",
			image: "/scout/containers/all.sif",
			want:  "/scout/containers/all.sif",
		},
		{
			name:     "absolute sif ignores imageDir",
			image:    "/scout/containers/all.sif",
			imageDir: "/other/dir",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif resolved against imageDir",
			image:    "all.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/all.sif",
		},
		{
			name:     "relative sif with subdirectory",
			image:    "gpu/predict.sif",
			imageDir: "/scout/containers",
			want:     "/scout/containers/gpu/predict.sif",
		},
		{
			name:  "relative sif without imageDir passed through",
			image: "all.sif",
			want:  "all.sif",
		},
		{
			name:     "registry image ignores imageDir",
			image:    "dxkb/esmfold:latest",
			imageDir: "/scout/containers",
			want:     "docker://dxkb/esmfold:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveApptainerImage(tt.image, tt.imageDir)
			if got != tt.want {
				t.Errorf("resolveApptainerImage(%q, %q) = %q, want %q", tt.image, tt.imageDir, got, tt.want)
			}
		})
	}
}

// TestExecuteLocal_LogCaptureBounded is an integration-style test through the
// local execute path (GoWe#134): a command that emits far more than
// maxLogCapture bytes to stdout and stderr must still produce a Result whose
// Stdout/Stderr equal exactly the tail the old bytes.Buffer+tailString
// approach would have produced, while the capture writer itself never grows
// past the bounded tailBuffer allocation (verified in tailwriter_test.go).
func TestExecuteLocal_LogCaptureBounded(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	workDir := t.TempDir()

	// Emit well over maxLogCapture (256KB) bytes to both stdout and stderr,
	// each ending in a distinctive, known sentinel so we can assert the exact
	// tail was preserved rather than merely "some truncated content".
	const totalBytes = maxLogCapture * 3
	script := fmt.Sprintf(
		`yes 0123456789 | head -c %d; printf 'STDOUT-END'; (yes 0123456789 | head -c %d; printf 'STDERR-END') 1>&2`,
		totalBytes, totalBytes,
	)

	tool := &cwl.CommandLineTool{}
	cmdResult := &cmdline.BuildResult{Command: []string{"sh", "-c", script}}
	opts := &Options{
		Tool:    tool,
		Command: cmdResult,
		Inputs:  map[string]any{},
		WorkDir: workDir,
		OutDir:  workDir,
	}

	e := NewExecutor(slog.Default())
	result, err := e.executeLocal(context.Background(), opts)
	if err != nil {
		t.Fatalf("executeLocal: %v", err)
	}

	// Reproduce the pre-fix behavior (buffer the full stream, then take the
	// tail) independently, by re-running the same script and feeding its full
	// output through a fresh tailBuffer, to confirm byte-for-byte parity.
	verify := exec.Command("sh", "-c", script)
	var fullStdout, fullStderr bytes.Buffer
	verify.Stdout = &fullStdout
	verify.Stderr = &fullStderr
	if err := verify.Run(); err != nil {
		t.Fatalf("verify run: %v", err)
	}
	wantStdout := newTailBuffer(maxLogCapture)
	wantStdout.Write(fullStdout.Bytes())
	wantStderr := newTailBuffer(maxLogCapture)
	wantStderr.Write(fullStderr.Bytes())

	if result.Stdout != wantStdout.String() {
		t.Errorf("Stdout mismatch: got %d bytes, want %d bytes (got suffix %q, want suffix %q)",
			len(result.Stdout), len(wantStdout.String()), tailSuffix(result.Stdout, 20), tailSuffix(wantStdout.String(), 20))
	}
	if result.Stderr != wantStderr.String() {
		t.Errorf("Stderr mismatch: got %d bytes, want %d bytes (got suffix %q, want suffix %q)",
			len(result.Stderr), len(wantStderr.String()), tailSuffix(result.Stderr, 20), tailSuffix(wantStderr.String(), 20))
	}
	if !strings.HasSuffix(result.Stdout, "STDOUT-END") {
		t.Errorf("Stdout lost the final sentinel: suffix %q", tailSuffix(result.Stdout, 20))
	}
	if !strings.HasSuffix(result.Stderr, "STDERR-END") {
		t.Errorf("Stderr lost the final sentinel: suffix %q", tailSuffix(result.Stderr, 20))
	}
	if !strings.HasPrefix(result.Stdout, "... [truncated] ...\n") {
		t.Errorf("Stdout missing truncation marker: prefix %q", result.Stdout[:min(30, len(result.Stdout))])
	}
}

func tailSuffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
