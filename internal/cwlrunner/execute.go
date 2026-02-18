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

// executeLocal executes a tool locally without Docker.
func (r *Runner) executeLocal(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any) (map[string]any, error) {
	r.logger.Info("executing locally", "command", cmdResult.Command)

	// Create working directory.
	workDir := filepath.Join(r.OutDir, "work")
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

	cmd := exec.CommandContext(ctx, cmdResult.Command[0], cmdResult.Command[1:]...)
	cmd.Dir = workDir

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

	// Run command.
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok && isSuccessCode(exitErr.ExitCode(), tool.SuccessCodes) {
			// Exit code is in success codes list.
		} else {
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	// Collect outputs.
	outputs, err := r.collectOutputs(tool, workDir, inputs)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return outputs, nil
}

// executeInDocker executes a tool in a Docker container.
func (r *Runner) executeInDocker(ctx context.Context, tool *cwl.CommandLineTool, cmdResult *cmdline.BuildResult, inputs map[string]any, dockerImage string) (map[string]any, error) {
	r.logger.Info("executing in Docker", "image", dockerImage, "command", cmdResult.Command)

	// Create directories.
	workDir := filepath.Join(r.OutDir, "work")
	tmpDir := filepath.Join(r.OutDir, "tmp")
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

	// Run Docker command.
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if ok && isSuccessCode(exitErr.ExitCode(), tool.SuccessCodes) {
			// Exit code is in success codes list.
		} else {
			return nil, fmt.Errorf("docker command failed: %w", err)
		}
	}

	// Collect outputs.
	outputs, err := r.collectOutputs(tool, workDir, inputs)
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

	// If path is already in workDir, nothing to do.
	absPath, _ := filepath.Abs(path)
	absWorkDir, _ := filepath.Abs(workDir)
	if strings.HasPrefix(absPath, absWorkDir) {
		return nil
	}

	// Create symlink in workDir.
	basename := filepath.Base(path)
	linkPath := filepath.Join(workDir, basename)

	// Check if link already exists.
	if _, err := os.Lstat(linkPath); err == nil {
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
func (r *Runner) collectOutputs(tool *cwl.CommandLineTool, workDir string, inputs map[string]any) (map[string]any, error) {
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
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle standard outputBinding
		if output.OutputBinding == nil {
			outputs[outputID] = nil
			continue
		}

		collected, err := r.collectOutputBinding(output.OutputBinding, output.Type, workDir, inputs, tool)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputID, err)
		}
		outputs[outputID] = collected
	}

	return outputs, nil
}

// collectOutputBinding collects files matching an output binding.
func (r *Runner) collectOutputBinding(binding *cwl.OutputBinding, outputType any, workDir string, inputs map[string]any, tool *cwl.CommandLineTool) (any, error) {
	// If there's an outputEval but no glob, evaluate it directly with self=null.
	if binding.Glob == nil {
		if binding.OutputEval != "" {
			ctx := cwlexpr.NewContext(inputs)
			ctx = ctx.WithSelf(nil)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = r.OutDir
			ctx = ctx.WithRuntime(runtime)

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
			var obj map[string]any
			var err error
			info, statErr := os.Stat(match)
			if statErr != nil {
				return nil, statErr
			}

			if info.IsDir() || expectDirectory {
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
		ctx = ctx.WithRuntime(runtime)

		evaluator := cwlexpr.NewEvaluator(nil)
		return evaluator.Evaluate(binding.OutputEval, ctx)
	}

	// Return single file or array.
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
		return t == "Directory" || t == "Directory?"
	case map[string]any:
		if typeName, ok := t["type"].(string); ok {
			return typeName == "Directory"
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
