package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/execution"
)

func TestFileStager_StageIn_FileScheme(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create source file.
	srcFile := filepath.Join(srcDir, "input.txt")
	if err := os.WriteFile(srcFile, []byte("test data"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := execution.NewFileStager("local")
	dstFile := filepath.Join(dstDir, "input.txt")

	err := stager.StageIn(context.Background(), "file://"+srcFile, dstFile)
	if err != nil {
		t.Fatalf("StageIn error: %v", err)
	}

	data, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "test data" {
		t.Errorf("content = %q, want test data", data)
	}
}

func TestFileStager_StageIn_BarePath(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "input.txt")
	if err := os.WriteFile(srcFile, []byte("bare path"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := execution.NewFileStager("local")
	dstFile := filepath.Join(dstDir, "input.txt")

	err := stager.StageIn(context.Background(), srcFile, dstFile)
	if err != nil {
		t.Fatalf("StageIn error: %v", err)
	}

	data, _ := os.ReadFile(dstFile)
	if string(data) != "bare path" {
		t.Errorf("content = %q, want bare path", data)
	}
}

func TestFileStager_StageIn_UnsupportedScheme(t *testing.T) {
	stager := execution.NewFileStager("local")
	err := stager.StageIn(context.Background(), "ws:///user@bvbrc/path", "/tmp/dest")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if !strings.Contains(err.Error(), "unsupported scheme") {
		t.Errorf("error = %q, want unsupported scheme message", err.Error())
	}
}

func TestFileStager_StageOut_Local(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "output.txt")
	if err := os.WriteFile(srcFile, []byte("result"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := execution.NewFileStager("local")
	loc, err := stager.StageOut(context.Background(), srcFile, "task_123")
	if err != nil {
		t.Fatalf("StageOut error: %v", err)
	}

	if !strings.HasPrefix(loc, "file://") {
		t.Errorf("location = %q, want file:// prefix", loc)
	}
	if !strings.Contains(loc, "output.txt") {
		t.Errorf("location = %q, want it to contain output.txt", loc)
	}
}

func TestFileStager_StageOut_SharedPath(t *testing.T) {
	srcDir := t.TempDir()
	sharedDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "result.txt")
	if err := os.WriteFile(srcFile, []byte("shared result"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := execution.NewFileStager("file://" + sharedDir)
	loc, err := stager.StageOut(context.Background(), srcFile, "task_456")
	if err != nil {
		t.Fatalf("StageOut error: %v", err)
	}

	if !strings.HasPrefix(loc, "file://") {
		t.Errorf("location = %q, want file:// prefix", loc)
	}

	// Verify file was copied to shared path.
	destFile := filepath.Join(sharedDir, "task_456", "result.txt")
	data, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("read shared file: %v", err)
	}
	if string(data) != "shared result" {
		t.Errorf("content = %q, want shared result", data)
	}
}
