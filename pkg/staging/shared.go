package staging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SharedFileStager handles files on shared/mounted filesystems (NFS, bind mounts, Lustre, GPFS).
// Instead of copying files, it can:
// - Translate paths (e.g., host path → container path)
// - Create symlinks for InitialWorkDirRequirement
// - Verify file accessibility
type SharedFileStager struct {
	// pathMap translates paths from one namespace to another.
	// Key is the source prefix, value is the target prefix.
	// Example: {"/host/data": "/container/data"}
	pathMap map[string]string

	// mode determines how files are staged.
	mode StageMode

	// stageOutDir is the base directory for stage-out operations.
	// If empty, "local" mode is used (files stay in place).
	stageOutDir string
}

// SharedFileStagerConfig holds configuration for SharedFileStager.
type SharedFileStagerConfig struct {
	// PathMap maps source paths to target paths.
	PathMap map[string]string

	// Mode determines staging behavior (Symlink, Reference, or Copy).
	Mode StageMode

	// StageOutDir is the base directory for output staging.
	StageOutDir string
}

// NewSharedFileStager creates a SharedFileStager with the given configuration.
func NewSharedFileStager(cfg SharedFileStagerConfig) *SharedFileStager {
	pathMap := cfg.PathMap
	if pathMap == nil {
		pathMap = make(map[string]string)
	}
	return &SharedFileStager{
		pathMap:     pathMap,
		mode:        cfg.Mode,
		stageOutDir: cfg.StageOutDir,
	}
}

// StageIn makes a file accessible at destPath using the configured mode.
// For shared filesystems, this typically means creating a symlink or verifying
// the file is already accessible.
func (s *SharedFileStager) StageIn(_ context.Context, location string, destPath string, opts StageOptions) error {
	scheme, srcPath := ParseLocationScheme(location)

	// Only handle file:// and bare paths.
	if scheme != "file" && scheme != "" {
		return fmt.Errorf("shared stager: unsupported scheme %q (only file:// supported)", scheme)
	}

	// Translate path if needed.
	localPath := s.translatePath(srcPath)

	// Verify file exists.
	info, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("shared file not accessible: %s (translated from %s): %w", localPath, srcPath, err)
	}

	// Determine effective mode (opts can override).
	mode := s.mode
	if opts.Mode != StageModeCopy {
		mode = opts.Mode
	}

	switch mode {
	case StageModeReference:
		// Reference mode: file is already accessible, nothing to do.
		// The caller should update input object's path to localPath if needed.
		return nil

	case StageModeSymlink:
		// Create symlink in workdir pointing to shared file.
		if info.IsDir() {
			return s.symlinkDir(localPath, destPath)
		}
		return Symlink(localPath, destPath)

	default:
		// Copy mode: fall back to copying the file.
		if info.IsDir() {
			return CopyDirectory(localPath, destPath)
		}
		return CopyFile(localPath, destPath)
	}
}

// StageOut copies or stages a file from srcPath to the configured destination.
// For shared filesystems, this might just return a file:// URI without copying.
func (s *SharedFileStager) StageOut(_ context.Context, srcPath string, taskID string, _ StageOptions) (string, error) {
	if s.stageOutDir == "" {
		// Local mode: return file:// URI pointing to the file in-place.
		absPath, err := filepath.Abs(srcPath)
		if err != nil {
			return "", fmt.Errorf("shared stager: abs path: %w", err)
		}
		return BuildLocation("file", absPath), nil
	}

	// Copy to stage-out directory.
	destDir := filepath.Join(s.stageOutDir, taskID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("shared stager: mkdir %s: %w", destDir, err)
	}

	destPath := filepath.Join(destDir, filepath.Base(srcPath))

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", fmt.Errorf("shared stager: stat %s: %w", srcPath, err)
	}

	if info.IsDir() {
		if err := CopyDirectory(srcPath, destPath); err != nil {
			return "", fmt.Errorf("shared stager: copy dir: %w", err)
		}
	} else {
		if err := CopyFile(srcPath, destPath); err != nil {
			return "", fmt.Errorf("shared stager: copy file: %w", err)
		}
	}

	return BuildLocation("file", destPath), nil
}

// Supports returns true for file:// and bare paths.
func (s *SharedFileStager) Supports(scheme string) bool {
	return scheme == "file" || scheme == ""
}

// TranslatedPath returns the translated path for a given source path.
// This is useful for callers who need to know the actual local path
// when using StageModeReference.
func (s *SharedFileStager) TranslatedPath(srcPath string) string {
	return s.translatePath(srcPath)
}

// translatePath applies path mapping to translate a path.
func (s *SharedFileStager) translatePath(srcPath string) string {
	for srcPrefix, dstPrefix := range s.pathMap {
		if strings.HasPrefix(srcPath, srcPrefix) {
			return dstPrefix + strings.TrimPrefix(srcPath, srcPrefix)
		}
	}
	return srcPath
}

// symlinkDir creates a symlink for a directory.
func (s *SharedFileStager) symlinkDir(srcDir, linkPath string) error {
	// Ensure parent exists.
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Remove existing link if it exists.
	if existing, err := os.Lstat(linkPath); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			// Check if it points to the same target.
			target, err := os.Readlink(linkPath)
			if err == nil && target == srcDir {
				return nil // Already correct.
			}
			// Different target - remove and recreate.
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("remove existing symlink: %w", err)
			}
		} else {
			// Not a symlink, can't replace.
			return fmt.Errorf("destination exists and is not a symlink: %s", linkPath)
		}
	}

	return os.Symlink(srcDir, linkPath)
}

// Verify interface compliance.
var _ Stager = (*SharedFileStager)(nil)
