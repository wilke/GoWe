package cli

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/server"
)

// startTestServer starts a skeleton server and returns the URL and a cleanup func.
func startTestServer(t *testing.T) string {
	t.Helper()
	srvLogger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := server.New(config.DefaultServerConfig(), srvLogger)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func testdataPath(rel string) string {
	return filepath.Join("..", "..", "testdata", rel)
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()

	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)

	err := root.Execute()
	return buf.String(), err
}

func TestSubmitCommand(t *testing.T) {
	url := startTestServer(t)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t,
		"--server", url,
		"submit", testdataPath("separate/pipeline.cwl"),
		"--inputs", testdataPath("separate/job.yml"),
	)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("submit error: %v\noutput: %s", err, output)
	}
	if !strings.Contains(output, "Workflow registered: wf_") {
		t.Errorf("expected 'Workflow registered: wf_' in output, got: %s", output)
	}
	if !strings.Contains(output, "Submission created: sub_") {
		t.Errorf("expected 'Submission created: sub_' in output, got: %s", output)
	}
}

func TestSubmitCommand_DryRun(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t,
		"--server", url,
		"submit", testdataPath("separate/pipeline.cwl"),
		"--inputs", testdataPath("separate/job.yml"),
		"--dry-run",
	)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("submit --dry-run error: %v\noutput: %s", err, output)
	}
	if !strings.Contains(output, "Dry-run:") {
		t.Errorf("expected 'Dry-run:' in output, got: %s", output)
	}
	if !strings.Contains(output, "No submission created") {
		t.Errorf("expected 'No submission created' in output, got: %s", output)
	}
}

func TestStatusCommand(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "status", "sub_test123")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(output, "sub_test123") {
		t.Errorf("expected submission ID in output, got: %s", output)
	}
	if !strings.Contains(output, "COMPLETED") {
		t.Errorf("expected COMPLETED state in output, got: %s", output)
	}
}

func TestListCommand(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "list")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(output, "ID") {
		t.Errorf("expected table header in output, got: %s", output)
	}
	if !strings.Contains(output, "COMPLETED") {
		t.Errorf("expected submission state in output, got: %s", output)
	}
}

func TestCancelCommand(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "cancel", "sub_test123")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("cancel error: %v", err)
	}
	if !strings.Contains(output, "CANCELLED") {
		t.Errorf("expected CANCELLED in output, got: %s", output)
	}
}

func TestLogsCommand(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "logs", "sub_test123")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("logs error: %v", err)
	}
	if !strings.Contains(output, "SPAdes") {
		t.Errorf("expected SPAdes in stdout output, got: %s", output)
	}
}

func TestAppsCommand(t *testing.T) {
	url := startTestServer(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "apps")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("apps error: %v", err)
	}
	if !strings.Contains(output, "GenomeAssembly2") {
		t.Errorf("expected GenomeAssembly2 in output, got: %s", output)
	}
}

func TestSubmitCommand_MissingFile(t *testing.T) {
	url := startTestServer(t)
	_, err := runCLI(t, "--server", url, "submit", "nonexistent.cwl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
