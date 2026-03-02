package staging

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedFileStager_StageIn_Copy(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create stager in copy mode (default).
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeCopy,
	})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file://"+srcFile, dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify file was copied.
	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read dest file: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("got content %q, want %q", content, "hello")
	}

	// Verify it's a regular file, not a symlink.
	info, err := os.Lstat(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected regular file, got symlink")
	}
}

func TestSharedFileStager_StageIn_Symlink(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create stager in symlink mode.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeSymlink,
	})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file://"+srcFile, dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify it's a symlink.
	info, err := os.Lstat(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected symlink, got regular file")
	}

	// Verify symlink points to correct target.
	target, err := os.Readlink(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if target != srcFile {
		t.Errorf("symlink target = %q, want %q", target, srcFile)
	}

	// Verify content is accessible through symlink.
	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read through symlink: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("got content %q, want %q", content, "hello")
	}
}

func TestSharedFileStager_StageIn_Reference(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination path (won't be used).
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create stager in reference mode.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeReference,
	})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file://"+srcFile, dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify nothing was created at destination.
	if _, err := os.Stat(dstFile); !os.IsNotExist(err) {
		t.Error("expected no file at destination in reference mode")
	}
}

func TestSharedFileStager_StageIn_PathTranslation(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "data", "test.txt")
	if err := os.MkdirAll(filepath.Dir(srcFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcFile, []byte("translated"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory.
	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create stager with path mapping.
	// Map /host/data -> srcDir/data
	stager := NewSharedFileStager(SharedFileStagerConfig{
		PathMap: map[string]string{
			"/host/data": filepath.Join(srcDir, "data"),
		},
		Mode: StageModeCopy,
	})

	ctx := context.Background()
	// Use the "host" path in location - should be translated.
	err := stager.StageIn(ctx, "file:///host/data/test.txt", dstFile, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify file was copied.
	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("read dest file: %v", err)
	}
	if string(content) != "translated" {
		t.Errorf("got content %q, want %q", content, "translated")
	}
}

func TestSharedFileStager_StageIn_Directory_Symlink(t *testing.T) {
	// Create a temp source directory with files.
	srcDir := t.TempDir()
	srcSubDir := filepath.Join(srcDir, "subdir")
	if err := os.MkdirAll(srcSubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcSubDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcSubDir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory.
	dstDir := t.TempDir()
	dstSubDir := filepath.Join(dstDir, "subdir")

	// Create stager in symlink mode.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeSymlink,
	})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file://"+srcSubDir, dstSubDir, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify it's a symlink to directory.
	info, err := os.Lstat(dstSubDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected symlink, got regular directory")
	}

	// Verify contents are accessible.
	content, err := os.ReadFile(filepath.Join(dstSubDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "a" {
		t.Errorf("got content %q, want %q", content, "a")
	}
}

func TestSharedFileStager_StageIn_Directory_Copy(t *testing.T) {
	// Create a temp source directory with files.
	srcDir := t.TempDir()
	srcSubDir := filepath.Join(srcDir, "subdir")
	if err := os.MkdirAll(srcSubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcSubDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory.
	dstDir := t.TempDir()
	dstSubDir := filepath.Join(dstDir, "subdir")

	// Create stager in copy mode.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeCopy,
	})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file://"+srcSubDir, dstSubDir, StageOptions{})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Verify it's a real directory, not a symlink.
	info, err := os.Lstat(dstSubDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected real directory, got symlink")
	}

	// Verify contents were copied.
	content, err := os.ReadFile(filepath.Join(dstSubDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "a" {
		t.Errorf("got content %q, want %q", content, "a")
	}
}

func TestSharedFileStager_StageOut_Local(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "output.txt")
	if err := os.WriteFile(srcFile, []byte("output"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create stager with no stage-out dir (local mode).
	stager := NewSharedFileStager(SharedFileStagerConfig{})

	ctx := context.Background()
	loc, err := stager.StageOut(ctx, srcFile, "task-123", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut failed: %v", err)
	}

	// Should return file:// URI pointing to original location.
	expected := "file://" + srcFile
	if loc != expected {
		t.Errorf("got location %q, want %q", loc, expected)
	}
}

func TestSharedFileStager_StageOut_SharedDir(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "output.txt")
	if err := os.WriteFile(srcFile, []byte("output"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create stage-out directory.
	stageOutDir := t.TempDir()

	// Create stager with stage-out dir.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		StageOutDir: stageOutDir,
	})

	ctx := context.Background()
	loc, err := stager.StageOut(ctx, srcFile, "task-456", StageOptions{})
	if err != nil {
		t.Fatalf("StageOut failed: %v", err)
	}

	// Should return file:// URI pointing to copied location.
	expectedPath := filepath.Join(stageOutDir, "task-456", "output.txt")
	expected := "file://" + expectedPath
	if loc != expected {
		t.Errorf("got location %q, want %q", loc, expected)
	}

	// Verify file was copied.
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(content) != "output" {
		t.Errorf("got content %q, want %q", content, "output")
	}
}

func TestSharedFileStager_StageIn_NotFound(t *testing.T) {
	stager := NewSharedFileStager(SharedFileStagerConfig{})

	ctx := context.Background()
	err := stager.StageIn(ctx, "file:///nonexistent/file.txt", "/tmp/dest.txt", StageOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestSharedFileStager_StageIn_UnsupportedScheme(t *testing.T) {
	stager := NewSharedFileStager(SharedFileStagerConfig{})

	ctx := context.Background()
	err := stager.StageIn(ctx, "s3://bucket/key", "/tmp/dest.txt", StageOptions{})
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestSharedFileStager_Supports(t *testing.T) {
	stager := NewSharedFileStager(SharedFileStagerConfig{})

	tests := []struct {
		scheme string
		want   bool
	}{
		{"file", true},
		{"", true},
		{"s3", false},
		{"http", false},
		{"shock", false},
	}

	for _, tt := range tests {
		got := stager.Supports(tt.scheme)
		if got != tt.want {
			t.Errorf("Supports(%q) = %v, want %v", tt.scheme, got, tt.want)
		}
	}
}

func TestSharedFileStager_TranslatedPath(t *testing.T) {
	stager := NewSharedFileStager(SharedFileStagerConfig{
		PathMap: map[string]string{
			"/host/data": "/container/data",
			"/mnt/nfs":   "/local/nfs",
		},
	})

	tests := []struct {
		input string
		want  string
	}{
		{"/host/data/file.txt", "/container/data/file.txt"},
		{"/host/data/subdir/file.txt", "/container/data/subdir/file.txt"},
		{"/mnt/nfs/shared.txt", "/local/nfs/shared.txt"},
		{"/other/path/file.txt", "/other/path/file.txt"}, // No mapping.
	}

	for _, tt := range tests {
		got := stager.TranslatedPath(tt.input)
		if got != tt.want {
			t.Errorf("TranslatedPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSharedFileStager_OptsOverrideMode(t *testing.T) {
	// Create a temp source file.
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	dstDir := t.TempDir()
	dstFile := filepath.Join(dstDir, "test.txt")

	// Create stager in copy mode.
	stager := NewSharedFileStager(SharedFileStagerConfig{
		Mode: StageModeCopy,
	})

	ctx := context.Background()
	// Override with symlink mode via opts.
	err := stager.StageIn(ctx, "file://"+srcFile, dstFile, StageOptions{
		Mode: StageModeSymlink,
	})
	if err != nil {
		t.Fatalf("StageIn failed: %v", err)
	}

	// Should be a symlink due to opts override.
	info, err := os.Lstat(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected symlink (opts override), got regular file")
	}
}
