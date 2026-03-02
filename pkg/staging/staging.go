// Package staging provides file staging interfaces and implementations for CWL execution.
// It supports staging files in/out of task working directories from various sources
// (local filesystem, S3, HTTP, etc.).
package staging

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Stager handles staging files in and out of task working directories.
type Stager interface {
	// StageIn downloads/copies a file from location to destPath.
	// Supports various URI schemes: file://, s3://, http://, ws://, etc.
	StageIn(ctx context.Context, location string, destPath string) error

	// StageOut uploads/copies a file from srcPath to the configured destination.
	// Returns the new location URI.
	StageOut(ctx context.Context, srcPath string, taskID string) (location string, err error)
}

// ParseLocationScheme extracts the scheme from a location string.
// Returns the scheme (e.g., "file", "s3", "http") and the path portion.
// If no scheme is present, returns empty scheme and the original string.
func ParseLocationScheme(location string) (scheme string, path string) {
	if idx := strings.Index(location, "://"); idx > 0 {
		return location[:idx], location[idx+3:]
	}
	// Handle file:/ (single slash) format.
	if strings.HasPrefix(location, "file:/") && !strings.HasPrefix(location, "file://") {
		return "file", location[6:]
	}
	return "", location
}

// BuildLocation constructs a location URI from scheme and path.
func BuildLocation(scheme string, path string) string {
	if scheme == "" || scheme == "file" {
		return "file://" + path
	}
	return scheme + "://" + path
}

// CopyFile copies a file from src to dst, creating parent directories as needed.
func CopyFile(src, dst string) error {
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

// CopyDirectory recursively copies a directory from src to dst.
func CopyDirectory(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := CopyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// ResolveSymlinks resolves symlinks in a path.
// On macOS, /tmp is a symlink to /private/tmp which can cause issues with Docker.
// Returns the original path if resolution fails.
func ResolveSymlinks(path string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return absPath
	}
	return resolved
}

// CollectInputMounts collects input files that need to be mounted in containers.
// Returns a map of hostPath -> containerPath.
// Host paths have symlinks resolved for Docker mounting.
// Container paths use the original path so commands work as expected.
func CollectInputMounts(inputs map[string]any) map[string]string {
	mounts := make(map[string]string)
	collectInputMountsValue(inputs, mounts)
	return mounts
}

// collectInputMountsValue recursively collects mount points from a value.
func collectInputMountsValue(v any, mounts map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				if path, ok := val["path"].(string); ok && filepath.IsAbs(path) {
					// Resolve symlinks for the host mount source,
					// but use original path as container target so commands work.
					resolved := ResolveSymlinks(path)
					mounts[resolved] = path
				}
				// Also collect secondary files.
				if secFiles, ok := val["secondaryFiles"].([]any); ok {
					for _, sf := range secFiles {
						collectInputMountsValue(sf, mounts)
					}
				}
				// Collect listing for Directory objects.
				if class == "Directory" {
					if listing, ok := val["listing"].([]any); ok {
						for _, item := range listing {
							collectInputMountsValue(item, mounts)
						}
					}
				}
			}
		}
		// Recurse into nested maps (for record types).
		for _, item := range val {
			collectInputMountsValue(item, mounts)
		}
	case []any:
		for _, item := range val {
			collectInputMountsValue(item, mounts)
		}
	}
}

// Symlink creates a symbolic link from linkPath pointing to target.
// If linkPath already exists as a symlink pointing to the same target, it's a no-op.
// If linkPath exists but points to a different target, it's removed and recreated.
func Symlink(target, linkPath string) error {
	// Check if link already exists.
	if existing, err := os.Readlink(linkPath); err == nil {
		if existing == target {
			return nil // Already correct.
		}
		// Different target - remove and recreate.
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove existing symlink: %w", err)
		}
	}

	// Create parent directories if needed.
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}

	return os.Symlink(target, linkPath)
}

// SymlinkToWorkDir creates a symlink in workDir pointing to srcPath if needed.
// Does nothing if srcPath is relative or already in workDir.
// Skips if a link with the same basename already exists.
func SymlinkToWorkDir(srcPath, workDir string) error {
	if !filepath.IsAbs(srcPath) {
		return nil // Relative paths don't need symlinking
	}

	absWorkDir, _ := filepath.Abs(workDir)
	if strings.HasPrefix(srcPath, absWorkDir) {
		return nil // Already in workdir
	}

	basename := filepath.Base(srcPath)
	linkPath := filepath.Join(workDir, basename)

	// Skip if link already exists.
	if _, err := os.Lstat(linkPath); err == nil {
		return nil
	}

	return os.Symlink(srcPath, linkPath)
}
