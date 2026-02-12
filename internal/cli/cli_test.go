package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/server"
	"github.com/me/gowe/internal/store"
)

// startTestServer starts a server with an in-memory SQLite store and returns the URL.
func startTestServer(t *testing.T) string {
	t.Helper()
	srvLogger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.NewSQLiteStore(":memory:", srvLogger)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	srv := server.New(config.DefaultServerConfig(), st, nil, srvLogger, server.WithTestApps([]map[string]any{
		{"id": "GenomeAssembly2", "label": "Genome Assembly", "description": "Assemble reads"},
		{"id": "GenomeAnnotation", "label": "Genome Annotation", "description": "Annotate a genome"},
		{"id": "ComprehensiveGenomeAnalysis", "label": "CGA", "description": "Assembly + Annotation + Analysis"},
	}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

// submitTestWorkflow creates a workflow + submission via HTTP and returns the submission ID.
func submitTestWorkflow(t *testing.T, serverURL string) string {
	t.Helper()

	srvLogger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
	c := NewClient(serverURL, srvLogger)

	// Read packed CWL directly.
	cwlData, err := os.ReadFile(testdataPath("packed/pipeline-packed.cwl"))
	if err != nil {
		t.Fatalf("read CWL: %v", err)
	}

	// Create workflow.
	wfResp, err := c.Post("/api/v1/workflows/", map[string]any{
		"name": "test-workflow",
		"cwl":  string(cwlData),
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	var wfData map[string]any
	json.Unmarshal(wfResp.Data, &wfData)
	wfID := wfData["id"].(string)

	// Create submission.
	subResp, err := c.Post("/api/v1/submissions/", map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"reads_r1": "test.fastq"},
	})
	if err != nil {
		t.Fatalf("create submission: %v", err)
	}
	var subData map[string]any
	json.Unmarshal(subResp.Data, &subData)
	return subData["id"].(string)
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
	subID := submitTestWorkflow(t, url)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "status", subID)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if !strings.Contains(output, subID) {
		t.Errorf("expected submission ID in output, got: %s", output)
	}
	if !strings.Contains(output, "PENDING") {
		t.Errorf("expected PENDING state in output, got: %s", output)
	}
}

func TestListCommand(t *testing.T) {
	url := startTestServer(t)
	submitTestWorkflow(t, url)

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
	if !strings.Contains(output, "PENDING") {
		t.Errorf("expected submission state in output, got: %s", output)
	}
}

func TestCancelCommand(t *testing.T) {
	url := startTestServer(t)
	subID := submitTestWorkflow(t, url)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "cancel", subID)

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
	subID := submitTestWorkflow(t, url)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_, err := runCLI(t, "--server", url, "logs", subID)

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if err != nil {
		t.Fatalf("logs error: %v", err)
	}
	// Newly created tasks have empty logs, but the command shows step headers.
	if !strings.Contains(output, "===") {
		t.Errorf("expected task log header in output, got: %s", output)
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
