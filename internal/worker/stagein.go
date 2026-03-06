package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/toolexec"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/staging"
)

// stageRemoteInputs downloads remote files (shock://, s3://, http://) to workDir
// and updates paths in inputs. Local files pass through unchanged.
func stageRemoteInputs(ctx context.Context, stager execution.Stager, inputs map[string]any, workDir string, logger *slog.Logger) error {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("create workdir: %w", err)
	}

	for inputID, v := range inputs {
		if err := stageInputValue(ctx, stager, inputID, v, workDir, logger); err != nil {
			return fmt.Errorf("input %s: %w", inputID, err)
		}
	}
	return nil
}

// stageInputValue recursively stages a single input value.
func stageInputValue(ctx context.Context, stager execution.Stager, inputID string, v any, workDir string, logger *slog.Logger) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return stageFileOrDirectory(ctx, stager, inputID, val, workDir, logger)
		}
		for k, nested := range val {
			if err := stageInputValue(ctx, stager, inputID+"."+k, nested, workDir, logger); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range val {
			if err := stageInputValue(ctx, stager, fmt.Sprintf("%s[%d]", inputID, i), item, workDir, logger); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageFileOrDirectory stages a File or Directory object.
func stageFileOrDirectory(ctx context.Context, stager execution.Stager, inputID string, obj map[string]any, workDir string, logger *slog.Logger) error {
	// Handle file literals.
	if _, err := fileliteral.MaterializeFileObject(obj); err != nil {
		return fmt.Errorf("materialize file literal for %s: %w", inputID, err)
	}

	// Handle Directory listings with file literals.
	if listing, ok := obj["listing"].([]any); ok {
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				if err := stageFileOrDirectory(ctx, stager, fmt.Sprintf("%s.listing[%d]", inputID, i), itemMap, workDir, logger); err != nil {
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
		return nil
	}

	scheme, path := cwl.ParseLocationScheme(location)

	// For local files with absolute paths, verify they exist.
	if (scheme == cwl.SchemeFile || scheme == "") && filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		logger.Warn("local file path not accessible",
			"input", inputID,
			"path", path,
			"hint", "consider using INPUT_PATH_MAP to translate host paths to container paths",
		)
		return nil
	}

	// For remote files, stage into workdir.
	if scheme != cwl.SchemeFile && scheme != "" {
		basename := filepath.Base(path)
		destPath := filepath.Join(workDir, basename)

		logger.Debug("staging remote file", "location", location, "dest", destPath)

		opts := staging.StageOptions{}
		if err := stager.StageIn(ctx, location, destPath, opts); err != nil {
			return fmt.Errorf("stage-in %s: %w", location, err)
		}

		obj["path"] = destPath
		obj["location"] = cwl.BuildLocation(cwl.SchemeFile, destPath)
	}

	// Stage secondary files.
	if secFiles, ok := obj["secondaryFiles"].([]any); ok {
		for i, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				if err := stageFileOrDirectory(ctx, stager, fmt.Sprintf("%s.secondaryFiles[%d]", inputID, i), sfMap, workDir, logger); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// toolexecGPU converts worker GPU config to toolexec GPU config.
func toolexecGPU(gpu GPUWorkerConfig) toolexec.GPUConfig {
	return toolexec.GPUConfig{
		Enabled:  gpu.Enabled,
		DeviceID: gpu.DeviceID,
	}
}
