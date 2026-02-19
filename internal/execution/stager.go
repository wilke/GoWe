package execution

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// Stager handles staging files in and out of task working directories.
type Stager interface {
	// StageIn downloads/copies a file from location to destPath.
	// Supports file://, shock://, s3://, ws:// schemes.
	StageIn(ctx context.Context, location string, destPath string) error

	// StageOut uploads/copies a file from srcPath to the configured destination.
	// Returns the new location URI.
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
		return fmt.Errorf("file stager: unsupported scheme %q for stage-in (use CompositeStager for remote schemes)", scheme)
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

// CompositeStager routes staging operations to scheme-specific handlers.
type CompositeStager struct {
	handlers map[string]Stager
	fallback Stager
}

// NewCompositeStager creates a CompositeStager with scheme handlers.
func NewCompositeStager(handlers map[string]Stager, fallback Stager) *CompositeStager {
	return &CompositeStager{
		handlers: handlers,
		fallback: fallback,
	}
}

// StageIn routes to the appropriate handler based on scheme.
func (s *CompositeStager) StageIn(ctx context.Context, location string, destPath string) error {
	scheme, _ := cwl.ParseLocationScheme(location)
	if handler, ok := s.handlers[scheme]; ok {
		return handler.StageIn(ctx, location, destPath)
	}
	if s.fallback != nil {
		return s.fallback.StageIn(ctx, location, destPath)
	}
	return fmt.Errorf("no stager registered for scheme %q", scheme)
}

// StageOut uses the fallback stager (typically file-based).
func (s *CompositeStager) StageOut(ctx context.Context, srcPath string, taskID string) (string, error) {
	if s.fallback != nil {
		return s.fallback.StageOut(ctx, srcPath, taskID)
	}
	return "", fmt.Errorf("no fallback stager configured for stage-out")
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

// stageInputs stages all File and Directory inputs into the workdir.
func (e *Engine) stageInputs(ctx context.Context, inputs map[string]any, workDir string) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create workdir: %w", err)
	}

	for inputID, v := range inputs {
		if err := e.stageInputValue(ctx, inputID, v, workDir); err != nil {
			return fmt.Errorf("input %s: %w", inputID, err)
		}
	}
	return nil
}

// stageInputValue recursively stages a single input value.
func (e *Engine) stageInputValue(ctx context.Context, inputID string, v any, workDir string) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return e.stageFileOrDirectory(ctx, inputID, val, workDir)
		}
		// Recursively handle nested objects (e.g., record fields).
		for k, nested := range val {
			if err := e.stageInputValue(ctx, inputID+"."+k, nested, workDir); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range val {
			if err := e.stageInputValue(ctx, fmt.Sprintf("%s[%d]", inputID, i), item, workDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageFileOrDirectory stages a File or Directory object.
func (e *Engine) stageFileOrDirectory(ctx context.Context, inputID string, obj map[string]any, workDir string) error {
	location := ""
	if loc, ok := obj["location"].(string); ok {
		location = loc
	} else if path, ok := obj["path"].(string); ok {
		location = path
	}

	if location == "" {
		return nil // Nothing to stage
	}

	scheme, path := cwl.ParseLocationScheme(location)

	// For local files with absolute paths, no staging needed.
	// The command line will reference them directly.
	if (scheme == cwl.SchemeFile || scheme == "") && filepath.IsAbs(path) {
		// For Docker execution, we'll handle mounting later.
		// For local execution, the path is already accessible.
		return nil
	}

	// For remote files, stage into workdir.
	if scheme != cwl.SchemeFile && scheme != "" {
		basename := filepath.Base(path)
		destPath := filepath.Join(workDir, basename)

		e.logger.Debug("staging remote file", "location", location, "dest", destPath)

		if err := e.stager.StageIn(ctx, location, destPath); err != nil {
			return fmt.Errorf("stage-in %s: %w", location, err)
		}

		// Update the object to point to the staged file.
		obj["path"] = destPath
		obj["location"] = cwl.BuildLocation(cwl.SchemeFile, destPath)
	}

	// Stage secondary files if present.
	if secFiles, ok := obj["secondaryFiles"].([]any); ok {
		for i, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				if err := e.stageFileOrDirectory(ctx, fmt.Sprintf("%s.secondaryFiles[%d]", inputID, i), sfMap, workDir); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// resolveSymlinks resolves symlinks in a path for Docker mounts.
// On macOS, /tmp is a symlink to /private/tmp which can cause issues with Docker.
func resolveSymlinks(path string) string {
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

// collectInputMounts collects input files that need to be mounted in Docker.
func collectInputMounts(inputs map[string]any) map[string]string {
	mounts := make(map[string]string)
	collectInputMountsValue(inputs, mounts)
	return mounts
}

// collectInputMountsValue recursively collects mount points from a value.
// It returns a map of hostPath → containerPath.
// The host path is resolved (symlinks evaluated) for Docker mounting,
// but the container path uses the original path so commands work as expected.
func collectInputMountsValue(v any, mounts map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				if path, ok := val["path"].(string); ok && filepath.IsAbs(path) {
					// Resolve symlinks for the host mount source,
					// but use original path as container target so commands work.
					resolved := resolveSymlinks(path)
					mounts[resolved] = path
				}
				// Also collect secondary files.
				if secFiles, ok := val["secondaryFiles"].([]any); ok {
					for _, sf := range secFiles {
						collectInputMountsValue(sf, mounts)
					}
				}
			}
		}
		// Recurse into nested maps.
		for _, item := range val {
			collectInputMountsValue(item, mounts)
		}
	case []any:
		for _, item := range val {
			collectInputMountsValue(item, mounts)
		}
	}
}

// symlink creates a symlink if file needs to be accessible from workDir.
func symlinkToWorkDir(srcPath, workDir string) error {
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
