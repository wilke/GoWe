// Package staging provides file staging interfaces and implementations for CWL execution.
// It supports staging files in/out of task working directories from various sources
// (local filesystem, S3, HTTP, Shock, etc.).
package staging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// StageMode determines how files are made accessible during staging.
type StageMode int

const (
	// StageModeCopy copies the file to the destination (default).
	StageModeCopy StageMode = iota
	// StageModeSymlink creates a symlink to the original file.
	StageModeSymlink
	// StageModeReference just updates the path, no copy/link (shared filesystem).
	StageModeReference
)

// String returns the string representation of the StageMode.
func (m StageMode) String() string {
	switch m {
	case StageModeCopy:
		return "copy"
	case StageModeSymlink:
		return "symlink"
	case StageModeReference:
		return "reference"
	default:
		return "unknown"
	}
}

// CredentialSet holds authentication credentials for staging operations.
type CredentialSet struct {
	Type        string `json:"type"`                        // "bearer", "basic", "header", "s3", "shock"
	Token       string `json:"token,omitempty"`             // for bearer/shock
	Username    string `json:"username,omitempty"`          // for basic
	Password    string `json:"password,omitempty"`          // for basic
	HeaderName  string `json:"header_name,omitempty"`       // for header
	HeaderValue string `json:"header_value,omitempty"`      // for header
	AccessKeyID string `json:"access_key_id,omitempty"`     // for S3
	SecretKey   string `json:"secret_access_key,omitempty"` // for S3
}

// StageOptions carries per-request configuration for staging operations.
type StageOptions struct {
	// Token is the authentication token (job-level or config fallback).
	Token string

	// Credentials provides a full credential set if needed.
	Credentials *CredentialSet

	// Headers are additional HTTP headers for the request.
	Headers map[string]string

	// Metadata is additional metadata for uploads (e.g., S3 tags, Shock attributes).
	Metadata map[string]string

	// Mode determines how files are staged (copy, symlink, reference).
	Mode StageMode

	// RelPath is the slash-separated destination path of the staged object
	// below the per-task root (<root>/<taskID>/<RelPath>). Empty means the
	// legacy layout (just the source basename). It must be local: not
	// absolute, no ".." escape, not ".". Stagers reject unsafe values with
	// ErrUnsafeRelPath.
	RelPath string

	// CopyTree is honoured only by DirectoryStager.StageOutDir: when set,
	// the whole directory tree is copied; when unset, only the destination
	// directory is created (its listing entries are staged individually).
	CopyTree bool
}

// ErrUnsafeRelPath is returned when StageOptions.RelPath is absolute, escapes
// the per-task root, or is otherwise not a clean local relative path.
var ErrUnsafeRelPath = errors.New("unsafe stage-out relative path")

// ErrDirectoryStagingUnsupported is returned by a stager (or router) that
// cannot stage directories.
var ErrDirectoryStagingUnsupported = errors.New("directory stage-out not supported")

// DirectoryStager is an optional Stager extension that stages a directory.
type DirectoryStager interface {
	// StageOutDir stages srcDir under the per-task root at opts.RelPath
	// (empty: basename of srcDir) and returns the new directory location.
	// With opts.CopyTree the whole tree is copied; otherwise only the
	// destination directory is created.
	StageOutDir(ctx context.Context, srcDir string, taskID string, opts StageOptions) (location string, err error)
}

// validateRelPath checks that rel is a clean, local, slash-separated path and
// returns it in OS form (cleaned).
func validateRelPath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("%w: empty", ErrUnsafeRelPath)
	}
	native := filepath.FromSlash(rel)
	if filepath.IsAbs(native) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%w: %q is absolute", ErrUnsafeRelPath, rel)
	}
	if !filepath.IsLocal(native) {
		return "", fmt.Errorf("%w: %q escapes the task root", ErrUnsafeRelPath, rel)
	}
	cleaned := filepath.Clean(native)
	if cleaned == "." {
		return "", fmt.Errorf("%w: %q", ErrUnsafeRelPath, rel)
	}
	return cleaned, nil
}

// ValidateRelPath reports whether rel is a safe StageOptions.RelPath value.
// The empty string is valid (legacy basename layout).
func ValidateRelPath(rel string) error {
	if rel == "" {
		return nil
	}
	_, err := validateRelPath(rel)
	return err
}

// stageOutRelPath returns the destination path below the per-task root for
// srcPath: relPath when set (validated), else the basename of srcPath.
func stageOutRelPath(srcPath, relPath string) (string, error) {
	if relPath == "" {
		return filepath.Base(srcPath), nil
	}
	return validateRelPath(relPath)
}

// StageOutRelSlash is like stageOutRelPath but returns a slash-separated path
// for URL/key based stagers.
func StageOutRelSlash(srcPath, relPath string) (string, error) {
	rel, err := stageOutRelPath(srcPath, relPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// Stager handles staging files in and out of task working directories.
type Stager interface {
	// StageIn downloads/copies a file from location to destPath.
	// Supports various URI schemes: file://, s3://, http://, shock://, etc.
	StageIn(ctx context.Context, location string, destPath string, opts StageOptions) error

	// StageOut uploads/copies a file from srcPath to the configured destination.
	// Returns the new location URI.
	StageOut(ctx context.Context, srcPath string, taskID string, opts StageOptions) (location string, err error)

	// Supports returns true if this stager handles the given scheme.
	Supports(scheme string) bool
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
//
// A completed copy and a correct copy are different claims: io.Copy returning
// nil only means the write syscalls were accepted, not that the bytes reached
// stable storage (buffered/NFS writers can report success and then lose data
// on a delayed-write error surfaced at Close/fsync) and not that the byte
// count matches. CopyFile therefore fsyncs and checks the Close error, then
// verifies src and dst are the same size before returning success.
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

	written, copyErr := io.Copy(out, in)
	if copyErr != nil {
		out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, copyErr)
	}

	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dst, err)
	}

	return verifyCopySize(src, dst, written)
}

// verifyCopySize confirms that src and dst report the same size, and that
// written (the byte count io.Copy reported transferring) matches dst's size
// on disk. A mismatch means the copy was accepted but truncated -- exactly
// the failure mode of the 256 KiB boundary transfers this guards against.
func verifyCopySize(src, dst string, written int64) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat src %s after copy: %w", src, err)
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		return fmt.Errorf("stat dst %s after copy: %w", dst, err)
	}

	if dstInfo.Size() != written {
		return fmt.Errorf("copy %s to %s: wrote %d bytes but dst is %d bytes on disk (truncated write)", src, dst, written, dstInfo.Size())
	}
	if srcInfo.Size() != dstInfo.Size() {
		return fmt.Errorf("copy %s to %s: size mismatch after copy: src=%d bytes dst=%d bytes", src, dst, srcInfo.Size(), dstInfo.Size())
	}

	return nil
}

// CopyTreeFollowingSymlinks recursively copies src to dst. Symlinks to files
// are followed (the copy holds the content, as CopyFile does for a single
// staged file). Symlinks to directories are followed only while the target
// resolves inside the tree rooted at src; a directory symlink that escapes
// the tree (a bind-mounted dataset, an input Directory on shared storage) is
// recreated as a symlink instead, so staging never duplicates data the tool
// did not produce. Symlink cycles are broken: a directory whose real path is
// already on the current descent stack is skipped.
func CopyTreeFollowingSymlinks(src, dst string) error {
	root, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	return copyTreeFollowing(src, dst, root, map[string]bool{})
}

func copyTreeFollowing(src, dst, root string, active map[string]bool) error {
	linfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		recreate := func() error {
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		}
		real, err := filepath.EvalSymlinks(src)
		if err != nil {
			return recreate() // dangling: keep the link rather than fail the tree
		}
		rinfo, err := os.Stat(real)
		if err != nil {
			return recreate()
		}
		if rinfo.IsDir() && !underDir(root, real) {
			return recreate()
		}
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return CopyFile(src, dst)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	if active[real] {
		return nil // symlink cycle
	}
	active[real] = true
	defer delete(active, real)

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTreeFollowing(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), root, active); err != nil {
			return err
		}
	}
	return nil
}

// underDir reports whether p is dir itself or lies below it (both cleaned
// absolute paths, no symlink resolution).
func underDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// stageOutDirTo implements DirectoryStager for the directory-backed stagers.
func stageOutDirTo(who, base, srcDir, taskID string, opts StageOptions) (string, error) {
	rel, err := stageOutRelPath(srcDir, opts.RelPath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", who, err)
	}
	dest := filepath.Join(base, taskID, rel)
	if opts.CopyTree {
		if err := CopyTreeFollowingSymlinks(srcDir, dest); err != nil {
			return "", fmt.Errorf("%s: copy dir: %w", who, err)
		}
	} else if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("%s: mkdir %s: %w", who, dest, err)
	}
	return BuildLocation("file", dest), nil
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
