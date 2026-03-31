package toolexec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/requirements"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/staging"
)

// stageInputFiles stages input files in the working directory.
func stageInputFiles(inputs map[string]any, workDir string) error {
	for _, v := range inputs {
		if err := stageInputValue(v, workDir); err != nil {
			return err
		}
	}
	return nil
}

// stageInputValue stages a single input value.
func stageInputValue(v any, workDir string) error {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				return stageFileOrDir(val, workDir)
			}
		}
	case []any:
		for _, item := range val {
			if err := stageInputValue(item, workDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageFileOrDir stages a File or Directory in the working directory.
// For local execution with absolute paths, staging is not needed since the
// command line already uses the full path.
func stageFileOrDir(obj map[string]any, workDir string) error {
	path := ""
	if p, ok := obj["path"].(string); ok {
		path = p
	} else if loc, ok := obj["location"].(string); ok {
		// Defensive: parse file:// URI to extract the actual path.
		// Normally obj["path"] is set by the upload pipeline or stagein,
		// but handle raw file:// URIs from direct API submissions.
		// Only strip file:// scheme; other schemes (http://, s3://) are
		// not local paths and should not be converted.
		if strings.HasPrefix(loc, "file://") {
			path = strings.TrimPrefix(loc, "file://")
			// Normalize: ensure leading slash for file:// URIs.
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
		} else if !strings.Contains(loc, "://") {
			// Plain path (no scheme) — use as-is.
			path = loc
		}
	}

	if path == "" {
		return nil
	}

	// If path is absolute, no staging needed - command line uses full path.
	if filepath.IsAbs(path) {
		return nil
	}

	// If path is already in workDir, nothing to do.
	absPath, _ := filepath.Abs(path)
	absWorkDir, _ := filepath.Abs(workDir)
	if strings.HasPrefix(absPath, absWorkDir) {
		return nil
	}

	// Create symlink in workDir for relative paths.
	basename := filepath.Base(path)
	linkPath := filepath.Join(workDir, basename)

	// Check if link already exists - use unique name if conflict.
	if _, err := os.Lstat(linkPath); err == nil {
		// Link already exists, skip - this can happen with same-named files.
		return nil
	}

	return os.Symlink(absPath, linkPath)
}

// CollectInputMounts collects input files that need to be mounted in Docker.
// Delegates to the shared staging package.
func CollectInputMounts(inputs map[string]any) map[string]string {
	return staging.CollectInputMounts(inputs)
}

// isSuccessCode checks if an exit code is in the success codes list.
func isSuccessCode(code int, successCodes []int) bool {
	if len(successCodes) == 0 {
		return code == 0
	}
	for _, sc := range successCodes {
		if code == sc {
			return true
		}
	}
	return false
}

// hasShellCommandRequirement checks if a tool has ShellCommandRequirement.
// Delegates to the shared requirements package.
func hasShellCommandRequirement(tool *cwl.CommandLineTool) bool {
	return requirements.HasShellCommand(tool)
}

// hasNetworkAccess checks if a tool has NetworkAccessRequirement with networkAccess: true.
// Delegates to the shared requirements package.
func hasNetworkAccess(tool *cwl.CommandLineTool) bool {
	return requirements.HasNetworkAccess(tool)
}

// hasStdoutOutput checks if the tool has any output of type "stdout".
func hasStdoutOutput(tool *cwl.CommandLineTool) bool {
	for _, output := range tool.Outputs {
		if output.Type == "stdout" {
			return true
		}
	}
	return false
}

// hasStderrOutput checks if the tool has any output of type "stderr".
func hasStderrOutput(tool *cwl.CommandLineTool) bool {
	for _, output := range tool.Outputs {
		if output.Type == "stderr" {
			return true
		}
	}
	return false
}

// isDirectoryType checks if the output type is Directory.
func isDirectoryType(outputType any) bool {
	switch t := outputType.(type) {
	case string:
		return t == "Directory" || t == "Directory?" || t == "Directory[]"
	case map[string]any:
		if typeName, ok := t["type"].(string); ok {
			return typeName == "Directory" || typeName == "array"
		}
		if items, ok := t["items"].(string); ok {
			return items == "Directory"
		}
	}
	return false
}

// isFileType checks if the output type is File.
func isFileType(outputType any) bool {
	switch t := outputType.(type) {
	case string:
		return t == "File" || t == "File?" || t == "File[]"
	case map[string]any:
		if typeName, ok := t["type"].(string); ok {
			if typeName == "File" {
				return true
			}
			if typeName == "array" {
				if items, ok := t["items"].(string); ok {
					return items == "File"
				}
			}
		}
	}
	return false
}

// isOptionalType checks if the output type is optional (allows null).
func isOptionalType(outputType any) bool {
	switch t := outputType.(type) {
	case string:
		return strings.HasSuffix(t, "?")
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "null" {
				return true
			}
		}
	}
	return false
}

// isArrayOutputType checks if the output type is an array type.
func isArrayOutputType(outputType any) bool {
	switch t := outputType.(type) {
	case string:
		return strings.HasSuffix(t, "[]")
	case map[string]any:
		if typeName, ok := t["type"].(string); ok {
			return typeName == "array"
		}
	}
	return false
}

// extractEnvVars extracts environment variables from EnvVarRequirement.
// Delegates to the shared requirements package.
func extractEnvVars(tool *cwl.CommandLineTool, inputs map[string]any, jobRequirements []any) map[string]string {
	return requirements.ExtractEnvVars(tool, inputs, jobRequirements)
}

// scanSymlinks walks a directory and returns a set of absolute paths that are symlinks.
// Uses os.Lstat (not Walk, which follows symlinks) to detect symlinks.
func scanSymlinks(dir string) map[string]bool {
	symlinks := make(map[string]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return symlinks
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if linfo, err := os.Lstat(path); err == nil && linfo.Mode()&os.ModeSymlink != 0 {
			abs, _ := filepath.Abs(path)
			symlinks[abs] = true
		}
		// Recurse into real directories (not symlinked ones).
		if entry.IsDir() {
			for k, v := range scanSymlinks(path) {
				symlinks[k] = v
			}
		}
	}
	return symlinks
}
