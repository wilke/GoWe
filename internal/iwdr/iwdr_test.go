package iwdr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func TestStageNoRequirement(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for tool without InitialWorkDirRequirement")
	}
}

func TestStageEmptyListing(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.ContainerMounts) != 0 {
		t.Errorf("expected no container mounts, got %d", len(result.ContainerMounts))
	}
}

func TestStageLiteralFile(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "hello.txt",
						"entry":     "Hello, World!",
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check file was created
	filePath := filepath.Join(workDir, "hello.txt")
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read staged file: %v", err)
	}
	if string(content) != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got %q", string(content))
	}
}

func TestStageFileObject(t *testing.T) {
	// Create a source file
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "source.txt")
	if err := os.WriteFile(srcFile, []byte("source content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"class":    "File",
						"path":     srcFile,
						"basename": "source.txt",
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{CopyForContainer: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check symlink was created
	stagedPath := filepath.Join(workDir, "source.txt")
	info, err := os.Lstat(stagedPath)
	if err != nil {
		t.Fatalf("failed to stat staged file: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("expected symlink, got regular file")
	}
}

func TestStageFileObjectCopy(t *testing.T) {
	// Create a source file
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "source.txt")
	if err := os.WriteFile(srcFile, []byte("source content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"class":    "File",
						"path":     srcFile,
						"basename": "source.txt",
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{CopyForContainer: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check file was copied (not symlinked)
	stagedPath := filepath.Join(workDir, "source.txt")
	info, err := os.Lstat(stagedPath)
	if err != nil {
		t.Fatalf("failed to stat staged file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected regular file, got symlink")
	}
	content, _ := os.ReadFile(stagedPath)
	if string(content) != "source content" {
		t.Errorf("expected 'source content', got %q", string(content))
	}
}

func TestStageWritableFile(t *testing.T) {
	// Create a source file
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "source.txt")
	if err := os.WriteFile(srcFile, []byte("source content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "writable.txt",
						"entry": map[string]any{
							"class":    "File",
							"path":     srcFile,
							"basename": "source.txt",
						},
						"writable": true,
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	// Writable files should always be copied even without CopyForContainer
	result, err := Stage(tool, inputs, workDir, StageOptions{CopyForContainer: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Check file was copied (not symlinked)
	stagedPath := filepath.Join(workDir, "writable.txt")
	info, err := os.Lstat(stagedPath)
	if err != nil {
		t.Fatalf("failed to stat staged file: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("expected regular file, got symlink")
	}
}

func TestStageAbsoluteEntrynameRequiresDocker(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "/absolute/path.txt",
						"entry":     "content",
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	// Should fail without DockerRequirement
	_, err := Stage(tool, inputs, workDir, StageOptions{})
	if err == nil {
		t.Error("expected error for absolute entryname without DockerRequirement")
	}
}

func TestStageAbsoluteEntrynameWithDocker(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Requirements: map[string]any{
			"DockerRequirement": map[string]any{
				"dockerPull": "alpine",
			},
			"InitialWorkDirRequirement": map[string]any{
				"listing": []any{
					map[string]any{
						"entryname": "/etc/myconfig.txt",
						"entry":     "config content",
					},
				},
			},
		},
	}
	inputs := map[string]any{}
	workDir := t.TempDir()

	result, err := Stage(tool, inputs, workDir, StageOptions{CopyForContainer: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.ContainerMounts) != 1 {
		t.Fatalf("expected 1 container mount, got %d", len(result.ContainerMounts))
	}
	if result.ContainerMounts[0].ContainerPath != "/etc/myconfig.txt" {
		t.Errorf("expected container path '/etc/myconfig.txt', got %q", result.ContainerMounts[0].ContainerPath)
	}
}

func TestHasDockerRequirement(t *testing.T) {
	tests := []struct {
		name     string
		tool     *cwl.CommandLineTool
		expected bool
	}{
		{
			name:     "no requirements",
			tool:     &cwl.CommandLineTool{},
			expected: false,
		},
		{
			name: "docker in requirements",
			tool: &cwl.CommandLineTool{
				Requirements: map[string]any{
					"DockerRequirement": map[string]any{},
				},
			},
			expected: true,
		},
		{
			name: "docker in hints only",
			tool: &cwl.CommandLineTool{
				Hints: map[string]any{
					"DockerRequirement": map[string]any{},
				},
			},
			expected: false, // hints don't count for absolute paths
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasDockerRequirement(tt.tool)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestUpdateInputPaths(t *testing.T) {
	workDir := "/work"
	stagedPaths := map[string]string{
		"/original/path/file.txt": "/work/renamed.txt",
	}

	inputs := map[string]any{
		"input1": map[string]any{
			"class":    "File",
			"path":     "/original/path/file.txt",
			"basename": "file.txt",
		},
	}

	UpdateInputPaths(inputs, workDir, stagedPaths)

	fileObj := inputs["input1"].(map[string]any)
	if fileObj["path"] != "/work/renamed.txt" {
		t.Errorf("expected path '/work/renamed.txt', got %v", fileObj["path"])
	}
	if fileObj["basename"] != "renamed.txt" {
		t.Errorf("expected basename 'renamed.txt', got %v", fileObj["basename"])
	}
}

func TestCopyFile(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "source.txt")
	if err := os.WriteFile(srcFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create source file: %v", err)
	}

	dstFile := filepath.Join(dstDir, "dest.txt")
	if err := copyFile(srcFile, dstFile); err != nil {
		t.Fatalf("copyFile failed: %v", err)
	}

	content, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("failed to read dest file: %v", err)
	}
	if string(content) != "test content" {
		t.Errorf("expected 'test content', got %q", string(content))
	}
}

func TestCopyDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := filepath.Join(t.TempDir(), "dest")

	// Create source structure
	subDir := filepath.Join(srcDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("failed to create file2: %v", err)
	}

	if err := copyDir(srcDir, dstDir); err != nil {
		t.Fatalf("copyDir failed: %v", err)
	}

	// Verify copies
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	if string(content1) != "content1" {
		t.Errorf("expected 'content1', got %q", string(content1))
	}

	content2, _ := os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
	if string(content2) != "content2" {
		t.Errorf("expected 'content2', got %q", string(content2))
	}
}

func TestCopyDirContents(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	// Create source files
	if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "file2.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("failed to create file2: %v", err)
	}

	if err := CopyDirContents(srcDir, dstDir); err != nil {
		t.Fatalf("CopyDirContents failed: %v", err)
	}

	// Verify copies
	content1, _ := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
	if string(content1) != "content1" {
		t.Errorf("expected 'content1', got %q", string(content1))
	}

	content2, _ := os.ReadFile(filepath.Join(dstDir, "file2.txt"))
	if string(content2) != "content2" {
		t.Errorf("expected 'content2', got %q", string(content2))
	}
}
