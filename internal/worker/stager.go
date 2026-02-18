package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/me/gowe/pkg/cwl"
)

// Stager handles staging files in and out of task working directories.
type Stager interface {
	StageIn(ctx context.Context, location string, destPath string) error
	StageOut(ctx context.Context, srcPath string, taskID string) (location string, err error)
}

// FileStager stages files using local filesystem operations.
// StageIn copies from file:// URIs. StageOut behavior depends on the mode:
//   - "local": returns file:// URI pointing to the file in-place (no copy)
//   - "file:///shared/path": copies to the shared path, returns file:// URI
type FileStager struct {
	mode string // "local" or "file:///shared/path"
}

// NewFileStager creates a FileStager with the given stage-out mode.
func NewFileStager(mode string) *FileStager {
	return &FileStager{mode: mode}
}

// StageIn copies a file from a file:// location to destPath.
func (s *FileStager) StageIn(_ context.Context, location string, destPath string) error {
	scheme, path := cwl.ParseLocationScheme(location)

	switch scheme {
	case cwl.SchemeFile, "":
		return copyFile(path, destPath)
	default:
		return fmt.Errorf("file stager: unsupported scheme %q for stage-in", scheme)
	}
}

// StageOut returns a file:// URI for the given source path.
// In "local" mode, returns a URI pointing directly to srcPath.
// In file:// mode, copies to the shared directory.
func (s *FileStager) StageOut(_ context.Context, srcPath string, taskID string) (string, error) {
	if s.mode == "local" {
		absPath, err := filepath.Abs(srcPath)
		if err != nil {
			return "", fmt.Errorf("file stager: abs path: %w", err)
		}
		return cwl.BuildLocation(cwl.SchemeFile, absPath), nil
	}

	// Parse mode as file:///shared/path
	scheme, basePath := cwl.ParseLocationScheme(s.mode)
	if scheme != cwl.SchemeFile {
		return "", fmt.Errorf("file stager: unsupported stage-out scheme %q", scheme)
	}

	destDir := filepath.Join(basePath, taskID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("file stager: mkdir %s: %w", destDir, err)
	}

	destPath := filepath.Join(destDir, filepath.Base(srcPath))
	if err := copyFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("file stager: copy to shared: %w", err)
	}

	return cwl.BuildLocation(cwl.SchemeFile, destPath), nil
}

// copyFile copies src to dst, creating parent directories as needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
