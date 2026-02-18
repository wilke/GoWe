package cwlrunner

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// executeLocalWithWorkDir executes a tool locally without Docker in the specified work directory.
func (r *Runner) executeLocalWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, workDir string) (map[string]any, error) {
	r.logger.Info("executing locally", "command", cmdResult.Command)

	// Create the working directory.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work directory: %w", err)
	}

	// Stage input files.
	if err := r.stageInputFiles(inputs, workDir); err != nil {
		return nil, fmt.Errorf("stage inputs: %w", err)
	}

	// Build command.
	if len(cmdResult.Command) == 0 {
		return nil, fmt.Errorf("empty command")
	}

	// Check for ShellCommandRequirement - run through shell if present.
	var cmd *exec.Cmd
	if hasShellCommandRequirement(tool) {
		// Join command parts and run through shell.
		cmdStr := strings.Join(cmdResult.Command, " ")
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	} else {
		cmd = exec.CommandContext(ctx, cmdResult.Command[0], cmdResult.Command[1:]...)
	}
	cmd.Dir = workDir

	// Set environment variables from EnvVarRequirement.
	cmd.Env = os.Environ() // Start with current environment
	envVars := extractEnvVars(tool, inputs)
	for name, value := range envVars {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", name, value))
	}

	// Handle stdin.
	if cmdResult.Stdin != "" {
		stdinPath := cmdResult.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(workDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin %s: %w", stdinPath, err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Determine stdout capture filename.
	stdoutCapture := cmdResult.Stdout
	if stdoutCapture == "" && hasStdoutOutput(tool) {
		stdoutCapture = "cwl.stdout.txt"
	}

	// Handle stdout - capture to file if specified or needed for output.
	var stdoutFile *os.File
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		var err error
		stdoutFile, err = os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		cmd.Stdout = io.Discard
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr - capture to file if specified or needed for output.
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = io.Discard
	}

	// Run command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				return nil, fmt.Errorf("command failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	// Collect outputs (passing exit code for runtime.exitCode).
	outputs, err := r.collectOutputs(tool, workDir, inputs, exitCode)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return outputs, nil
}

// executeInDockerWithWorkDir executes a tool in a Docker container with the specified work directory.
func (r *Runner) executeInDockerWithWorkDir(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string, workDir string) (map[string]any, error) {
	r.logger.Info("executing in Docker", "image", dockerImage, "command", cmdResult.Command)

	// Create directories for this execution.
	tmpDir := workDir + "_tmp"
	for _, dir := range []string{workDir, tmpDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	// Stage input files.
	if err := r.stageInputFiles(inputs, workDir); err != nil {
		return nil, fmt.Errorf("stage inputs: %w", err)
	}

	// Build Docker command.
	dockerArgs := []string{"run", "--rm", "-i"}

	// Mount working directory (resolve symlinks for macOS /tmp -> /private/tmp).
	absWorkDir := resolveSymlinks(workDir)
	dockerArgs = append(dockerArgs, "-v", absWorkDir+":/var/spool/cwl:rw")
	dockerArgs = append(dockerArgs, "-w", "/var/spool/cwl")

	// Mount tmp directory.
	absTmpDir := resolveSymlinks(tmpDir)
	dockerArgs = append(dockerArgs, "-v", absTmpDir+":/tmp:rw")

	// Mount input files that are outside working directory.
	mounts := collectInputMounts(inputs)
	for hostPath, containerPath := range mounts {
		dockerArgs = append(dockerArgs, "-v", hostPath+":"+containerPath+":ro")
	}

	// Add image.
	dockerArgs = append(dockerArgs, dockerImage)

	// Add tool command.
	dockerArgs = append(dockerArgs, cmdResult.Command...)

	r.logger.Debug("docker command", "args", dockerArgs)

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)

	// Handle stdin.
	if cmdResult.Stdin != "" {
		stdinPath := cmdResult.Stdin
		if !filepath.IsAbs(stdinPath) {
			stdinPath = filepath.Join(workDir, stdinPath)
		}
		stdin, err := os.Open(stdinPath)
		if err != nil {
			return nil, fmt.Errorf("open stdin: %w", err)
		}
		defer stdin.Close()
		cmd.Stdin = stdin
	}

	// Determine stdout capture filename.
	stdoutCapture := cmdResult.Stdout
	if stdoutCapture == "" && hasStdoutOutput(tool) {
		stdoutCapture = "cwl.stdout.txt"
	}

	// Handle stdout - capture to file if specified or needed for output.
	var stdoutFile *os.File
	if stdoutCapture != "" {
		stdoutPath := filepath.Join(workDir, stdoutCapture)
		var err error
		stdoutFile, err = os.Create(stdoutPath)
		if err != nil {
			return nil, fmt.Errorf("create stdout file: %w", err)
		}
		defer stdoutFile.Close()
		cmd.Stdout = stdoutFile
	} else {
		// Discard stdout to keep JSON output clean.
		cmd.Stdout = io.Discard
	}

	// Determine stderr capture filename.
	stderrCapture := cmdResult.Stderr
	if stderrCapture == "" && hasStderrOutput(tool) {
		stderrCapture = "cwl.stderr.txt"
	}

	// Handle stderr - capture to file if specified or needed for output.
	var stderrFile *os.File
	if stderrCapture != "" {
		stderrPath := filepath.Join(workDir, stderrCapture)
		var err error
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return nil, fmt.Errorf("create stderr file: %w", err)
		}
		defer stderrFile.Close()
		cmd.Stderr = stderrFile
	} else {
		cmd.Stderr = io.Discard
	}

	// Run Docker command and capture exit code.
	var exitCode int
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok {
			exitCode = exitErr.ExitCode()
			if !isSuccessCode(exitCode, tool.SuccessCodes) {
				return nil, fmt.Errorf("docker command failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("docker command failed: %w", err)
		}
	}

	// Collect outputs (passing exit code for runtime.exitCode).
	outputs, err := r.collectOutputs(tool, workDir, inputs, exitCode)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return outputs, nil
}

// stageInputFiles stages input files in the working directory.
func (r *Runner) stageInputFiles(inputs map[string]any, workDir string) error {
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

// collectInputMounts collects input files that need to be mounted in Docker.
func collectInputMounts(inputs map[string]any) map[string]string {
	mounts := make(map[string]string)
	for _, v := range inputs {
		collectInputMountsValue(v, mounts)
	}
	return mounts
}

// resolveSymlinks resolves symlinks in a path for Docker mounts.
// On macOS, /tmp is a symlink to /private/tmp which can cause issues with Docker.
// Always returns an absolute path.
func resolveSymlinks(path string) string {
	// First make absolute.
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	// Then resolve symlinks.
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return absPath
	}
	return resolved
}

// collectInputMountsValue collects mount points from a value.
func collectInputMountsValue(v any, mounts map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				if path, ok := val["path"].(string); ok {
					if filepath.IsAbs(path) {
						// Resolve symlinks for Docker mount compatibility.
						resolved := resolveSymlinks(path)
						mounts[resolved] = resolved
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

// collectOutputs collects tool outputs from the working directory.
// exitCode is passed for use in outputEval via runtime.exitCode.
func (r *Runner) collectOutputs(tool *cwl.CommandLineTool, workDir string, inputs map[string]any, exitCode int) (map[string]any, error) {
	outputs := make(map[string]any)

	// Check for cwl.output.json first - per CWL spec, if this file exists,
	// it provides THE complete output object.
	cwlOutputPath := filepath.Join(workDir, "cwl.output.json")
	if data, err := os.ReadFile(cwlOutputPath); err == nil {
		if err := jsonUnmarshal(data, &outputs); err != nil {
			return nil, fmt.Errorf("parse cwl.output.json: %w", err)
		}
		// Process File/Directory objects to resolve paths and add metadata.
		processOutputObjects(outputs, workDir)
		return outputs, nil
	}

	// Collect outputs based on type and outputBinding.
	for outputID, output := range tool.Outputs {

		// Handle stdout type - check tool.Stdout or use default filename
		if output.Type == "stdout" {
			stdoutFile := tool.Stdout
			if stdoutFile == "" {
				stdoutFile = "cwl.stdout.txt" // default CWL stdout filename
			} else if cwlexpr.IsExpression(stdoutFile) {
				// Evaluate stdout filename expression.
				ctx := cwlexpr.NewContext(inputs)
				evaluator := cwlexpr.NewEvaluator(nil)
				evaluated, err := evaluator.EvaluateString(stdoutFile, ctx)
				if err == nil {
					stdoutFile = evaluated
				}
			}
			stdoutPath := filepath.Join(workDir, stdoutFile)
			if fileInfo, err := os.Stat(stdoutPath); err == nil && !fileInfo.IsDir() {
				fileObj, err := createFileObject(stdoutPath, false)
				if err != nil {
					return nil, fmt.Errorf("output %s: %w", outputID, err)
				}
				// Apply format if specified.
				r.applyOutputFormat(fileObj, output.Format, inputs)
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle stderr type - check tool.Stderr or use default filename
		if output.Type == "stderr" {
			stderrFile := tool.Stderr
			if stderrFile == "" {
				stderrFile = "cwl.stderr.txt" // default CWL stderr filename
			} else if cwlexpr.IsExpression(stderrFile) {
				// Evaluate stderr filename expression.
				ctx := cwlexpr.NewContext(inputs)
				evaluator := cwlexpr.NewEvaluator(nil)
				evaluated, err := evaluator.EvaluateString(stderrFile, ctx)
				if err == nil {
					stderrFile = evaluated
				}
			}
			stderrPath := filepath.Join(workDir, stderrFile)
			if fileInfo, err := os.Stat(stderrPath); err == nil && !fileInfo.IsDir() {
				fileObj, err := createFileObject(stderrPath, false)
				if err != nil {
					return nil, fmt.Errorf("output %s: %w", outputID, err)
				}
				// Apply format if specified.
				r.applyOutputFormat(fileObj, output.Format, inputs)
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle standard outputBinding
		if output.OutputBinding == nil {
			outputs[outputID] = nil
			continue
		}

		collected, err := r.collectOutputBinding(output.OutputBinding, output.Type, workDir, inputs, tool, exitCode)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputID, err)
		}

		// Apply format to collected File objects.
		r.applyFormatToOutput(collected, output.Format, inputs)
		outputs[outputID] = collected
	}

	return outputs, nil
}

// collectOutputBinding collects files matching an output binding.
func (r *Runner) collectOutputBinding(binding *cwl.OutputBinding, outputType any, workDir string, inputs map[string]any, tool *cwl.CommandLineTool, exitCode int) (any, error) {
	// If there's an outputEval but no glob, evaluate it directly with self=null.
	if binding.Glob == nil {
		if binding.OutputEval != "" {
			ctx := cwlexpr.NewContext(inputs)
			ctx = ctx.WithSelf(nil)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = r.OutDir
			runtime.ExitCode = exitCode
			ctx = ctx.WithRuntime(runtime)
			ctx = ctx.WithOutputEval() // Enable exitCode access

			evaluator := cwlexpr.NewEvaluator(nil)
			return evaluator.Evaluate(binding.OutputEval, ctx)
		}
		return nil, nil
	}

	// Evaluate glob patterns.
	patterns := getGlobPatterns(binding.Glob, inputs, tool, workDir)
	if len(patterns) == 0 {
		return nil, nil
	}

	// Check if output type is Directory.
	expectDirectory := isDirectoryType(outputType)
	expectFile := isFileType(outputType)

	var collected []map[string]any
	for _, pattern := range patterns {
		// Determine the glob path - use absolute path if pattern is already absolute
		// or starts with workDir, otherwise join with workDir.
		var globPath string
		if filepath.IsAbs(pattern) {
			globPath = pattern
		} else if strings.HasPrefix(pattern, workDir) {
			globPath = pattern
		} else {
			globPath = filepath.Join(workDir, pattern)
		}
		matches, err := filepath.Glob(globPath)
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", pattern, err)
		}

		for _, match := range matches {
			info, statErr := os.Stat(match)
			if statErr != nil {
				return nil, statErr
			}

			// Type checking: raise error if types don't match.
			if expectFile && info.IsDir() {
				return nil, fmt.Errorf("type error: output type is File but glob matched directory %q", match)
			}
			if expectDirectory && !info.IsDir() {
				return nil, fmt.Errorf("type error: output type is Directory but glob matched file %q", match)
			}

			var obj map[string]any
			var err error
			if info.IsDir() {
				obj, err = createDirectoryObject(match)
			} else {
				obj, err = createFileObject(match, binding.LoadContents)
			}
			if err != nil {
				return nil, err
			}
			collected = append(collected, obj)
		}
	}

	// Apply outputEval if present.
	if binding.OutputEval != "" {
		ctx := cwlexpr.NewContext(inputs)
		ctx = ctx.WithSelf(collected)
		runtime := cwlexpr.DefaultRuntimeContext()
		runtime.OutDir = r.OutDir
		runtime.ExitCode = exitCode
		ctx = ctx.WithRuntime(runtime)
		ctx = ctx.WithOutputEval() // Enable exitCode access

		evaluator := cwlexpr.NewEvaluator(nil)
		return evaluator.Evaluate(binding.OutputEval, ctx)
	}

	// Check if output type is an array type.
	isArrayType := isArrayOutputType(outputType)

	// Return single file or array based on type.
	if isArrayType {
		// Always return array for array types.
		return collected, nil
	}
	if len(collected) == 1 {
		return collected[0], nil
	}
	return collected, nil
}

// getGlobPatterns extracts glob patterns from the binding.
func getGlobPatterns(glob any, inputs map[string]any, tool *cwl.CommandLineTool, workDir string) []string {
	switch g := glob.(type) {
	case string:
		// Check if it's an expression.
		if cwlexpr.IsExpression(g) {
			ctx := cwlexpr.NewContext(inputs)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = workDir
			ctx = ctx.WithRuntime(runtime)
			evaluator := cwlexpr.NewEvaluator(nil)
			result, err := evaluator.Evaluate(g, ctx)
			if err != nil {
				return nil
			}
			return getGlobPatterns(result, inputs, tool, workDir)
		}
		return []string{g}
	case []string:
		return g
	case []any:
		var patterns []string
		for _, item := range g {
			if s, ok := item.(string); ok {
				patterns = append(patterns, s)
			}
		}
		return patterns
	}
	return nil
}

// createFileObject creates a CWL File object from a path.
func createFileObject(path string, loadContents bool) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	absPath, _ := filepath.Abs(path)
	basename := filepath.Base(path)
	nameroot, nameext := splitNameExtension(basename)

	// Compute SHA1 checksum.
	checksum, err := computeChecksum(path)
	if err != nil {
		return nil, fmt.Errorf("compute checksum: %w", err)
	}

	obj := map[string]any{
		"class":    "File",
		"location": "file://" + absPath,
		"path":     absPath,
		"basename": basename,
		"dirname":  filepath.Dir(absPath),
		"nameroot": nameroot,
		"nameext":  nameext,
		"size":     info.Size(),
		"checksum": checksum,
	}

	if loadContents {
		// CWL spec: loadContents is limited to 64KB (65536 bytes).
		const maxLoadContentsSize = 64 * 1024
		if info.Size() > maxLoadContentsSize {
			return nil, fmt.Errorf("loadContents: file %q is %d bytes, exceeds 64KB limit", path, info.Size())
		}
		content, err := loadFileContents(path)
		if err != nil {
			return nil, err
		}
		obj["contents"] = content
	}

	return obj, nil
}

// computeChecksum computes the SHA1 checksum of a file.
func computeChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("sha1$%x", h.Sum(nil)), nil
}

// splitNameExtension splits a filename into nameroot and nameext.
func splitNameExtension(basename string) (string, string) {
	for i := len(basename) - 1; i > 0; i-- {
		if basename[i] == '.' {
			return basename[:i], basename[i:]
		}
	}
	return basename, ""
}

// loadFileContents loads the first 64KB of a file.
func loadFileContents(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Read up to 64KB.
	buf := make([]byte, 64*1024)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}

// jsonUnmarshal is a helper for JSON unmarshaling.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// processOutputObjects recursively processes File/Directory objects in outputs
// to resolve relative paths and add metadata (checksum, size, etc.).
func processOutputObjects(outputs map[string]any, workDir string) {
	for k, v := range outputs {
		outputs[k] = processOutputValue(v, workDir)
	}
}

// processOutputValue processes a single output value recursively.
func processOutputValue(v any, workDir string) any {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			return processFileOutput(val, workDir)
		}
		if class, ok := val["class"].(string); ok && class == "Directory" {
			return processDirectoryOutput(val, workDir)
		}
		// Recursively process nested maps.
		for k, item := range val {
			val[k] = processOutputValue(item, workDir)
		}
		return val
	case []any:
		for i, item := range val {
			val[i] = processOutputValue(item, workDir)
		}
		return val
	default:
		return v
	}
}

// processFileOutput processes a File object from cwl.output.json.
// Resolves relative paths and adds metadata.
func processFileOutput(obj map[string]any, workDir string) map[string]any {
	// Resolve path if relative.
	if path, ok := obj["path"].(string); ok {
		if !filepath.IsAbs(path) {
			obj["path"] = filepath.Join(workDir, path)
		}
	}

	// Resolve location if relative.
	if loc, ok := obj["location"].(string); ok {
		if !filepath.IsAbs(loc) && !strings.Contains(loc, "://") {
			obj["location"] = filepath.Join(workDir, loc)
		}
	}

	// If location is not set but path is, derive location.
	if _, hasLoc := obj["location"]; !hasLoc {
		if path, ok := obj["path"].(string); ok {
			obj["location"] = path
		}
	}

	// Get the actual path for metadata.
	path := ""
	if p, ok := obj["path"].(string); ok {
		path = p
	} else if loc, ok := obj["location"].(string); ok {
		path = loc
	}

	if path == "" {
		return obj
	}

	// Add file metadata if not present.
	info, err := os.Stat(path)
	if err != nil {
		return obj
	}

	if _, hasSize := obj["size"]; !hasSize {
		obj["size"] = info.Size()
	}

	if _, hasChecksum := obj["checksum"]; !hasChecksum {
		checksum, err := computeChecksum(path)
		if err == nil {
			obj["checksum"] = checksum
		}
	}

	if _, hasBasename := obj["basename"]; !hasBasename {
		obj["basename"] = filepath.Base(path)
	}

	return obj
}

// processDirectoryOutput processes a Directory object from cwl.output.json.
func processDirectoryOutput(obj map[string]any, workDir string) map[string]any {
	// Resolve path if relative.
	if path, ok := obj["path"].(string); ok {
		if !filepath.IsAbs(path) {
			obj["path"] = filepath.Join(workDir, path)
		}
	}

	// Resolve location if relative.
	if loc, ok := obj["location"].(string); ok {
		if !filepath.IsAbs(loc) && !strings.Contains(loc, "://") {
			obj["location"] = filepath.Join(workDir, loc)
		}
	}

	// If location is not set but path is, derive location.
	if _, hasLoc := obj["location"]; !hasLoc {
		if path, ok := obj["path"].(string); ok {
			obj["location"] = path
		}
	}

	return obj
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

// createDirectoryObject creates a CWL Directory object from a path.
func createDirectoryObject(path string) (map[string]any, error) {
	absPath, _ := filepath.Abs(path)
	basename := filepath.Base(path)

	obj := map[string]any{
		"class":    "Directory",
		"location": "file://" + absPath,
		"path":     absPath,
		"basename": basename,
	}

	// Build listing of directory contents.
	entries, err := os.ReadDir(path)
	if err != nil {
		return obj, nil // Return without listing if can't read directory.
	}

	var listing []map[string]any
	for _, entry := range entries {
		entryPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			dirObj, err := createDirectoryObject(entryPath)
			if err == nil {
				listing = append(listing, dirObj)
			}
		} else {
			fileObj, err := createFileObject(entryPath, false)
			if err == nil {
				listing = append(listing, fileObj)
			}
		}
	}

	// Always include listing (empty array if no contents).
	obj["listing"] = listing

	return obj, nil
}

// applyOutputFormat adds the format field to a File object.
// format can be a string (possibly with namespace prefix or expression) or nil.
// inputs is the resolved inputs for evaluating expressions.
func (r *Runner) applyOutputFormat(fileObj map[string]any, format any, inputs map[string]any) {
	if format == nil {
		return
	}

	formatStr, ok := format.(string)
	if !ok {
		return
	}

	// Check if format is an expression (e.g., $(inputs.input.format)).
	if cwlexpr.IsExpression(formatStr) {
		ctx := cwlexpr.NewContext(inputs)
		evaluator := cwlexpr.NewEvaluator(nil)
		result, err := evaluator.Evaluate(formatStr, ctx)
		if err == nil {
			if resultStr, ok := result.(string); ok {
				formatStr = resultStr
			}
		}
	}

	// Resolve namespace prefix if present (e.g., "edam:format_2330").
	resolvedFormat := r.resolveNamespacePrefix(formatStr)
	fileObj["format"] = resolvedFormat
}

// applyFormatToOutput applies format to collected output objects (single or array).
func (r *Runner) applyFormatToOutput(output any, format any, inputs map[string]any) {
	if format == nil || output == nil {
		return
	}

	switch val := output.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			r.applyOutputFormat(val, format, inputs)
		}
	case []map[string]any:
		for _, item := range val {
			if class, ok := item["class"].(string); ok && class == "File" {
				r.applyOutputFormat(item, format, inputs)
			}
		}
	case []any:
		for _, item := range val {
			if itemMap, ok := item.(map[string]any); ok {
				if class, ok := itemMap["class"].(string); ok && class == "File" {
					r.applyOutputFormat(itemMap, format, inputs)
				}
			}
		}
	}
}

// resolveNamespacePrefix resolves a namespace prefix to a full URI.
// e.g., "edam:format_2330" -> "http://edamontology.org/format_2330"
func (r *Runner) resolveNamespacePrefix(s string) string {
	if r.namespaces == nil {
		return s
	}

	// Look for colon separator (but not http://, https://, file://).
	idx := strings.Index(s, ":")
	if idx <= 0 {
		return s
	}

	// Skip if it looks like a full URI.
	prefix := s[:idx]
	if prefix == "http" || prefix == "https" || prefix == "file" {
		return s
	}

	// Look up the prefix in namespaces.
	if uri, ok := r.namespaces[prefix]; ok {
		return uri + s[idx+1:]
	}

	return s
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
