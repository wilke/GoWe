package toolexec

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/pkg/cwl"
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
		path = loc
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
func CollectInputMounts(inputs map[string]any) map[string]string {
	mounts := make(map[string]string)
	for _, v := range inputs {
		collectInputMountsValue(v, mounts)
	}
	return mounts
}

// collectInputMountsValue collects mount points from a value.
// It returns a map of hostPath -> containerPath.
// The host path is resolved (symlinks evaluated) for Docker mounting,
// but the container path uses the original path so commands work as expected.
func collectInputMountsValue(v any, mounts map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				if path, ok := val["path"].(string); ok {
					if filepath.IsAbs(path) {
						// Resolve symlinks for Docker mount source,
						// but use original path as container target.
						resolved := ResolveSymlinks(path)
						mounts[resolved] = path
					}
				}
				// Also collect secondary files for File objects.
				if class == "File" {
					if secFiles, ok := val["secondaryFiles"].([]any); ok {
						for _, sf := range secFiles {
							collectInputMountsValue(sf, mounts)
						}
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
	case []any:
		for _, item := range val {
			collectInputMountsValue(item, mounts)
		}
	}
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
func hasShellCommandRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["ShellCommandRequirement"]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints["ShellCommandRequirement"]; ok {
			return true
		}
	}
	return false
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

// extractEnvVars extracts environment variables from EnvVarRequirement in hints/requirements.
func extractEnvVars(tool *cwl.CommandLineTool, inputs map[string]any) map[string]string {
	envVars := make(map[string]string)

	// Check requirements first (takes precedence)
	if envReq := getEnvVarRequirement(tool.Requirements); envReq != nil {
		processEnvDef(envReq, envVars, inputs)
	}

	// Check hints
	if envReq := getEnvVarRequirement(tool.Hints); envReq != nil {
		// Only add hints if not already in requirements
		for k, v := range processEnvDef(envReq, make(map[string]string), inputs) {
			if _, exists := envVars[k]; !exists {
				envVars[k] = v
			}
		}
	}

	return envVars
}

// getEnvVarRequirement extracts EnvVarRequirement from hints or requirements map.
func getEnvVarRequirement(reqMap map[string]any) map[string]any {
	if reqMap == nil {
		return nil
	}
	if req, ok := reqMap["EnvVarRequirement"].(map[string]any); ok {
		return req
	}
	return nil
}

// processEnvDef processes envDef from EnvVarRequirement and adds to envVars map.
func processEnvDef(envReq map[string]any, envVars map[string]string, inputs map[string]any) map[string]string {
	envDef, ok := envReq["envDef"]
	if !ok {
		return envVars
	}

	// envDef can be an array or map
	switch defs := envDef.(type) {
	case []any:
		for _, def := range defs {
			if m, ok := def.(map[string]any); ok {
				name, _ := m["envName"].(string)
				value, _ := m["envValue"].(string)
				if name != "" {
					// TODO: Evaluate expressions in envValue if needed
					envVars[name] = value
				}
			}
		}
	case map[string]any:
		for name, val := range defs {
			if value, ok := val.(string); ok {
				envVars[name] = value
			}
		}
	}

	return envVars
}
