package staging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

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
// Respects StageOptions.Mode for symlink vs copy behavior.
func (s *FileStager) StageIn(_ context.Context, location string, destPath string, opts StageOptions) error {
	scheme, path := ParseLocationScheme(location)

	switch scheme {
	case "file", "":
		// Check if source exists
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("file stager: source not found: %w", err)
		}

		switch opts.Mode {
		case StageModeReference:
			// Reference mode: file is already accessible, nothing to do.
			// The caller should update the path in the input object.
			return nil
		case StageModeSymlink:
			return Symlink(path, destPath)
		default:
			// Default to copy mode
			return CopyFile(path, destPath)
		}
	default:
		return fmt.Errorf("file stager: unsupported scheme %q for stage-in (use CompositeStager for remote schemes)", scheme)
	}
}

// StageOut returns a file:// URI for the given source path.
// In "local" mode, returns a URI pointing directly to srcPath.
// In file:// mode, copies to the shared directory.
func (s *FileStager) StageOut(_ context.Context, srcPath string, taskID string, opts StageOptions) (string, error) {
	if s.mode == "local" {
		absPath, err := filepath.Abs(srcPath)
		if err != nil {
			return "", fmt.Errorf("file stager: abs path: %w", err)
		}
		return BuildLocation("file", absPath), nil
	}

	// Parse mode as file:///shared/path
	scheme, basePath := ParseLocationScheme(s.mode)
	if scheme != "file" {
		return "", fmt.Errorf("file stager: unsupported stage-out scheme %q", scheme)
	}

	rel, err := stageOutRelPath(srcPath, opts.RelPath)
	if err != nil {
		return "", fmt.Errorf("file stager: %w", err)
	}
	destPath := filepath.Join(basePath, taskID, rel)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", fmt.Errorf("file stager: mkdir %s: %w", filepath.Dir(destPath), err)
	}
	if err := CopyFile(srcPath, destPath); err != nil {
		return "", fmt.Errorf("file stager: copy to shared: %w", err)
	}

	return BuildLocation("file", destPath), nil
}

// StageOutDir stages a directory. In "local" mode the directory stays in place.
// Otherwise the destination <base>/<taskID>/<RelPath> is created, and with
// opts.CopyTree the whole tree is copied (following symlinks).
func (s *FileStager) StageOutDir(_ context.Context, srcDir string, taskID string, opts StageOptions) (string, error) {
	if s.mode == "local" {
		absPath, err := filepath.Abs(srcDir)
		if err != nil {
			return "", fmt.Errorf("file stager: abs path: %w", err)
		}
		return BuildLocation("file", absPath), nil
	}
	scheme, basePath := ParseLocationScheme(s.mode)
	if scheme != "file" {
		return "", fmt.Errorf("file stager: unsupported stage-out scheme %q", scheme)
	}
	return stageOutDirTo("file stager", basePath, srcDir, taskID, opts)
}

// Supports returns true for file and empty schemes.
func (s *FileStager) Supports(scheme string) bool {
	return scheme == "file" || scheme == ""
}

// Verify interface compliance.
var (
	_ Stager          = (*FileStager)(nil)
	_ DirectoryStager = (*FileStager)(nil)
)
