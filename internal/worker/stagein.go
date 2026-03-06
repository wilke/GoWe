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

	// Use a staging directory OUTSIDE the workDir for top-level directory reconstruction.
	// This prevents staging artifacts (like _inputs/) from polluting the tool's
	// working directory and appearing in `find .` or `ls` output.
	stagingDir := filepath.Join(filepath.Dir(workDir), filepath.Base(workDir)+"_staging")

	for inputID, v := range inputs {
		if err := stageInputValue(ctx, stager, inputID, v, workDir, stagingDir, logger); err != nil {
			return fmt.Errorf("input %s: %w", inputID, err)
		}
	}
	return nil
}

// stageInputValue recursively stages a single input value.
// stagingDir is used for top-level directory reconstruction (outside workDir).
func stageInputValue(ctx context.Context, stager execution.Stager, inputID string, v any, workDir, stagingDir string, logger *slog.Logger) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return stageFileOrDirectory(ctx, stager, inputID, val, workDir, stagingDir, logger)
		}
		for k, nested := range val {
			if err := stageInputValue(ctx, stager, inputID+"."+k, nested, workDir, stagingDir, logger); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range val {
			if err := stageInputValue(ctx, stager, fmt.Sprintf("%s[%d]", inputID, i), item, workDir, stagingDir, logger); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageFileOrDirectory stages a File or Directory object.
// stagingDir, when non-empty, is used for top-level directory reconstruction
// (kept outside workDir to avoid polluting the tool's working directory).
func stageFileOrDirectory(ctx context.Context, stager execution.Stager, inputID string, obj map[string]any, workDir, stagingDir string, logger *slog.Logger) error {
	// Handle file literals.
	if _, err := fileliteral.MaterializeFileObject(obj); err != nil {
		return fmt.Errorf("materialize file literal for %s: %w", inputID, err)
	}

	location := ""
	if loc, ok := obj["location"].(string); ok {
		location = loc
	} else if path, ok := obj["path"].(string); ok {
		location = path
	}

	class, _ := obj["class"].(string)

	// For Directories with a listing but no accessible location,
	// reconstruct the directory from listing entries.
	// reconstructDirectoryFromListing handles everything (file literals, remote staging,
	// local file copying), so we skip individual listing entry processing.
	if class == "Directory" {
		if listing, ok := obj["listing"].([]any); ok && len(listing) > 0 {
			basename, _ := obj["basename"].(string)
			if basename == "" {
				basename = inputID
			}

			// Check if the location is missing or inaccessible.
			needsReconstruct := location == ""
			if !needsReconstruct {
				_, localPath := cwl.ParseLocationScheme(location)
				if _, err := os.Stat(localPath); err != nil {
					needsReconstruct = true
				}
			}

			if needsReconstruct {
				// Use stagingDir (outside workDir) when available to avoid polluting
				// the tool's working directory. Fall back to workDir for secondaryFile
				// directories that need adjacency to primary files.
				stageDir := stagingDir
				if stageDir == "" {
					stageDir = workDir
				}
				dirPath := filepath.Join(stageDir, basename)
				if err := reconstructDirectoryFromListing(ctx, stager, inputID, obj, dirPath, logger); err != nil {
					return err
				}
				return nil
			}
		}
	}

	// For accessible directories, process listing entries for nested file literals
	// and secondaryFiles. Skip this for directories being reconstructed (handled above).
	if listing, ok := obj["listing"].([]any); ok {
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				if err := stageFileOrDirectory(ctx, stager, fmt.Sprintf("%s.listing[%d]", inputID, i), itemMap, workDir, "", logger); err != nil {
					return err
				}
			}
		}
	}

	if location == "" {
		return nil
	}

	scheme, path := cwl.ParseLocationScheme(location)

	// For local files with absolute paths, verify they exist but don't return
	// early — fall through to process secondaryFiles.
	localFileExists := false
	if (scheme == cwl.SchemeFile || scheme == "") && filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			localFileExists = true
		} else {
			logger.Warn("local file path not accessible",
				"input", inputID,
				"path", path,
				"hint", "consider using INPUT_PATH_MAP to translate host paths to container paths",
			)
		}
	}

	// For remote files, stage into workdir.
	if !localFileExists && scheme != cwl.SchemeFile && scheme != "" {
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

	// Stage secondary files. Use the primary file's dirname as the staging
	// base so reconstructed directories end up adjacent to the primary file
	// (required for $(inputs.file.dirname)/secondary_dir references).
	if secFiles, ok := obj["secondaryFiles"].([]any); ok {
		primaryDir := workDir
		if p, ok := obj["path"].(string); ok && p != "" {
			primaryDir = filepath.Dir(p)
		}
		for i, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				if err := stageFileOrDirectory(ctx, stager, fmt.Sprintf("%s.secondaryFiles[%d]", inputID, i), sfMap, primaryDir, "", logger); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// reconstructDirectoryFromListing creates a directory at dirPath and populates it
// from the listing entries. Nested subdirectories are staged directly under dirPath.
func reconstructDirectoryFromListing(ctx context.Context, stager execution.Stager, inputID string, obj map[string]any, dirPath string, logger *slog.Logger) error {
	listing, _ := obj["listing"].([]any)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("create listing dir %s: %w", dirPath, err)
	}

	for i, item := range listing {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}

		itemClass, _ := itemMap["class"].(string)
		itemBasename, _ := itemMap["basename"].(string)
		childID := fmt.Sprintf("%s.listing[%d]", inputID, i)

		if itemClass == "Directory" {
			// Reconstruct subdirectory directly under dirPath (no _inputs).
			subListing, hasListing := itemMap["listing"].([]any)
			if hasListing && len(subListing) > 0 {
				subDirPath := filepath.Join(dirPath, itemBasename)
				if err := reconstructDirectoryFromListing(ctx, stager, childID, itemMap, subDirPath, logger); err != nil {
					return err
				}
				continue
			}
			// Directory with no listing — check if location is accessible.
			loc := ""
			if l, ok := itemMap["location"].(string); ok {
				loc = l
			} else if l, ok := itemMap["path"].(string); ok {
				loc = l
			}
			if loc != "" {
				_, localPath := cwl.ParseLocationScheme(loc)
				if _, err := os.Stat(localPath); err == nil {
					// Symlink the accessible directory into place.
					destInDir := filepath.Join(dirPath, itemBasename)
					if destInDir != localPath {
						_ = os.Symlink(localPath, destInDir)
					}
					itemMap["path"] = destInDir
					itemMap["location"] = cwl.BuildLocation(cwl.SchemeFile, destInDir)
					continue
				}
			}
			// Empty directory with no accessible location — just create it.
			subDirPath := filepath.Join(dirPath, itemBasename)
			_ = os.MkdirAll(subDirPath, 0o755)
			itemMap["path"] = subDirPath
			itemMap["location"] = cwl.BuildLocation(cwl.SchemeFile, subDirPath)
			continue
		}

		// File entry — stage it, then copy/link into the directory.
		// Handle file literals first.
		if _, err := fileliteral.MaterializeFileObject(itemMap); err != nil {
			return fmt.Errorf("materialize file literal for %s: %w", childID, err)
		}

		itemLoc := ""
		if l, ok := itemMap["path"].(string); ok {
			itemLoc = l
		} else if l, ok := itemMap["location"].(string); ok {
			itemLoc = l
		}

		if itemLoc == "" {
			continue
		}

		scheme, srcPath := cwl.ParseLocationScheme(itemLoc)

		// Stage remote files first.
		if scheme != cwl.SchemeFile && scheme != "" {
			destPath := filepath.Join(dirPath, itemBasename)
			logger.Debug("staging remote file in dir", "location", itemLoc, "dest", destPath)
			opts := staging.StageOptions{}
			if err := stager.StageIn(ctx, itemLoc, destPath, opts); err != nil {
				return fmt.Errorf("stage-in %s: %w", itemLoc, err)
			}
			itemMap["path"] = destPath
			itemMap["location"] = cwl.BuildLocation(cwl.SchemeFile, destPath)
			continue
		}

		// Local file — verify it exists and copy into the directory.
		if filepath.IsAbs(srcPath) {
			if _, err := os.Stat(srcPath); err == nil {
				if itemBasename == "" {
					itemBasename = filepath.Base(srcPath)
				}
				destInDir := filepath.Join(dirPath, itemBasename)
				if destInDir != srcPath {
					_ = staging.CopyFile(srcPath, destInDir)
				}
				itemMap["path"] = destInDir
				itemMap["location"] = cwl.BuildLocation(cwl.SchemeFile, destInDir)
			}
		}
	}

	obj["path"] = dirPath
	obj["location"] = cwl.BuildLocation(cwl.SchemeFile, dirPath)
	// Note: We intentionally preserve the listing on the object. Even though
	// the directory has been reconstructed on disk, some tests need the listing
	// metadata (e.g., for deep_listing). PopulateDirectoryListings will handle
	// cleanup for no_listing cases via removeListingFromInput.
	logger.Debug("reconstructed directory from listing",
		"input", inputID,
		"dir", dirPath,
	)
	return nil
}

// toolexecGPU converts worker GPU config to toolexec GPU config.
func toolexecGPU(gpu GPUWorkerConfig) toolexec.GPUConfig {
	return toolexec.GPUConfig{
		Enabled:  gpu.Enabled,
		DeviceID: gpu.DeviceID,
	}
}
