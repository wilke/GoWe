package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/staging"
)

// Stager is a type alias for the shared staging.Stager interface.
type Stager = staging.Stager

// FileStager is a type alias for the shared staging.FileStager.
type FileStager = staging.FileStager

// CompositeStager is a type alias for the shared staging.CompositeStager.
type CompositeStager = staging.CompositeStager

// NewFileStager creates a FileStager with the given stage-out mode.
func NewFileStager(mode string) *staging.FileStager {
	return staging.NewFileStager(mode)
}

// NewCompositeStager creates a CompositeStager with scheme handlers.
func NewCompositeStager(handlers map[string]Stager, fallback Stager) *staging.CompositeStager {
	return staging.NewCompositeStager(handlers, fallback)
}

// copyFile copies src to dst, creating parent directories as needed.
// Delegates to the shared staging package.
func copyFile(src, dst string) error {
	return staging.CopyFile(src, dst)
}

// resolveSymlinks resolves symlinks in a path for Docker mounts.
// Delegates to the shared staging package.
func resolveSymlinks(path string) string {
	return staging.ResolveSymlinks(path)
}

// collectInputMounts collects input files that need to be mounted in containers.
// Delegates to the shared staging package.
func collectInputMounts(inputs map[string]any) map[string]string {
	return staging.CollectInputMounts(inputs)
}

// symlinkToWorkDir creates a symlink in workDir pointing to srcPath if needed.
// Delegates to the shared staging package.
func symlinkToWorkDir(srcPath, workDir string) error {
	return staging.SymlinkToWorkDir(srcPath, workDir)
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
	// Handle file literals: File objects with "contents" but no path/location.
	// These need to be materialized as actual files before staging.
	if _, err := fileliteral.MaterializeFileObject(obj); err != nil {
		return fmt.Errorf("materialize file literal for %s: %w", inputID, err)
	}

	// Handle Directory listings with file literals.
	if listing, ok := obj["listing"].([]any); ok {
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				if err := e.stageFileOrDirectory(ctx, fmt.Sprintf("%s.listing[%d]", inputID, i), itemMap, workDir); err != nil {
					return err
				}
			}
		}
	}

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

	// Stage secondary files.
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
