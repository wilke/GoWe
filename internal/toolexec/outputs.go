package toolexec

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// collectOutputs collects tool outputs from the working directory.
// exitCode is passed for use in outputEval via runtime.exitCode.
// CollectOutputs collects tool outputs from the working directory.
// This function is exported so that both cwl-runner and worker can use it.
func (e *Executor) CollectOutputs(tool *cwl.CommandLineTool, workDir string, inputs map[string]any, exitCode int, outDir string, namespaces map[string]string) (map[string]any, error) {
	outputs := make(map[string]any)

	// Check for cwl.output.json first - per CWL spec, if this file exists,
	// it provides THE complete output object.
	cwlOutputPath := filepath.Join(workDir, "cwl.output.json")
	if data, err := os.ReadFile(cwlOutputPath); err == nil {
		if err := json.Unmarshal(data, &outputs); err != nil {
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
				applyOutputFormat(fileObj, output.Format, inputs, namespaces)
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
				applyOutputFormat(fileObj, output.Format, inputs, namespaces)
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle record type with field-level outputBindings.
		if output.Type == "record" && len(output.OutputRecordFields) > 0 {
			recordOutput, err := e.collectRecordOutput(output.OutputRecordFields, workDir, inputs, tool, exitCode, outDir, namespaces)
			if err != nil {
				return nil, fmt.Errorf("output %s: %w", outputID, err)
			}
			outputs[outputID] = recordOutput
			continue
		}

		// Handle standard outputBinding
		if output.OutputBinding == nil {
			outputs[outputID] = nil
			continue
		}

		collected, err := e.collectOutputBinding(output.OutputBinding, output.Type, workDir, inputs, tool, exitCode, outDir)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputID, err)
		}

		// Add secondaryFiles to collected output.
		if len(output.SecondaryFiles) > 0 {
			collected = e.addSecondaryFilesToOutput(collected, output.SecondaryFiles, workDir, inputs)
		}

		// Apply format to collected File objects.
		applyFormatToOutput(collected, output.Format, inputs, namespaces)
		outputs[outputID] = collected
	}

	return outputs, nil
}

// collectRecordOutput collects output for a record type with field-level outputBindings.
func (e *Executor) collectRecordOutput(fields []cwl.OutputRecordField, workDir string, inputs map[string]any, tool *cwl.CommandLineTool, exitCode int, outDir string, namespaces map[string]string) (map[string]any, error) {
	record := make(map[string]any)

	for _, field := range fields {
		if field.OutputBinding == nil {
			record[field.Name] = nil
			continue
		}

		collected, err := e.collectOutputBinding(field.OutputBinding, field.Type, workDir, inputs, tool, exitCode, outDir)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.Name, err)
		}

		// Add secondaryFiles to collected File objects.
		if len(field.SecondaryFiles) > 0 {
			collected = e.addSecondaryFilesToOutput(collected, field.SecondaryFiles, workDir, inputs)
		}

		record[field.Name] = collected
	}

	return record, nil
}

// addSecondaryFilesToOutput adds secondary files to File objects in the output.
func (e *Executor) addSecondaryFilesToOutput(output any, schemas []cwl.SecondaryFileSchema, workDir string, inputs map[string]any) any {
	switch v := output.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			// Add secondary files to this File object.
			e.addSecondaryFiles(v, schemas, workDir, inputs)
		}
		return v

	case []map[string]any:
		// Process each File object in the array.
		for _, item := range v {
			if class, ok := item["class"].(string); ok && class == "File" {
				e.addSecondaryFiles(item, schemas, workDir, inputs)
			}
		}
		return v

	case []any:
		// Process each item in the array.
		for i, item := range v {
			v[i] = e.addSecondaryFilesToOutput(item, schemas, workDir, inputs)
		}
		return v

	default:
		return output
	}
}

// addSecondaryFiles adds secondary files to a File object.
func (e *Executor) addSecondaryFiles(fileObj map[string]any, schemas []cwl.SecondaryFileSchema, workDir string, inputs map[string]any) {
	path, ok := fileObj["path"].(string)
	if !ok {
		return
	}

	var secondaryFiles []any
	for _, schema := range schemas {
		pattern := schema.Pattern
		if pattern == "" {
			continue
		}

		// Collect paths to add for this pattern (may be multiple for array results).
		var pathsToAdd []string

		// Check if pattern is a JavaScript expression.
		if cwlexpr.IsExpression(pattern) {
			// Evaluate the expression with 'self' set to the file object.
			evaluator := cwlexpr.NewEvaluator(nil)
			ctx := cwlexpr.NewContext(inputs).WithSelf(fileObj)
			result, err := evaluator.Evaluate(pattern, ctx)
			if err != nil {
				e.logger.Debug("failed to evaluate secondaryFiles expression", "pattern", pattern, "error", err)
				continue
			}

			// Extract the secondary file path(s) from the result.
			pathsToAdd = extractSecondaryPaths(result, filepath.Dir(path))
		} else {
			// Apply pattern to derive secondary file path.
			// Pattern starting with ^ means remove extension first.
			secondaryPath := path
			for strings.HasPrefix(pattern, "^") {
				// Remove one extension.
				ext := filepath.Ext(secondaryPath)
				if ext != "" {
					secondaryPath = strings.TrimSuffix(secondaryPath, ext)
				}
				pattern = strings.TrimPrefix(pattern, "^")
			}
			secondaryPath = secondaryPath + pattern
			pathsToAdd = append(pathsToAdd, secondaryPath)
		}

		// Add each path as a secondary file or directory.
		for _, secondaryPath := range pathsToAdd {
			if secondaryPath == "" {
				continue
			}

			// Check if secondary file/directory exists.
			info, err := os.Stat(secondaryPath)
			if err != nil {
				continue // Skip missing secondary files.
			}

			// Create File or Directory object.
			var secObj map[string]any
			if info.IsDir() {
				secObj, err = createDirectoryObject(secondaryPath)
			} else {
				secObj, err = createFileObject(secondaryPath, false)
			}
			if err != nil {
				continue
			}
			secondaryFiles = append(secondaryFiles, secObj)
		}
	}

	if len(secondaryFiles) > 0 {
		fileObj["secondaryFiles"] = secondaryFiles
	}
}

// extractSecondaryPaths extracts file paths from an expression result.
// The result can be a string, File object, or array of strings/File objects.
func extractSecondaryPaths(result any, baseDir string) []string {
	var paths []string

	switch v := result.(type) {
	case string:
		// Result is a filename - construct full path in same directory.
		paths = append(paths, filepath.Join(baseDir, v))
	case map[string]any:
		// Result is a File object - extract path or construct from basename.
		if p, ok := v["path"].(string); ok {
			paths = append(paths, p)
		} else if loc, ok := v["location"].(string); ok {
			paths = append(paths, loc)
		} else if bn, ok := v["basename"].(string); ok {
			paths = append(paths, filepath.Join(baseDir, bn))
		}
	case []any:
		// Array of results - process each element.
		for _, item := range v {
			paths = append(paths, extractSecondaryPaths(item, baseDir)...)
		}
	}

	return paths
}

// collectOutputBinding collects files matching an output binding.
func (e *Executor) collectOutputBinding(binding *cwl.OutputBinding, outputType any, workDir string, inputs map[string]any, tool *cwl.CommandLineTool, exitCode int, outDir string) (any, error) {
	// If there's an outputEval but no glob, evaluate it directly with self=null.
	if binding.Glob == nil {
		if binding.OutputEval != "" {
			ctx := cwlexpr.NewContext(inputs)
			ctx = ctx.WithSelf(nil)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = outDir
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
		runtime.OutDir = outDir
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
func applyOutputFormat(fileObj map[string]any, format any, inputs map[string]any, namespaces map[string]string) {
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
	resolvedFormat := resolveNamespacePrefix(formatStr, namespaces)
	fileObj["format"] = resolvedFormat
}

// applyFormatToOutput applies format to collected output objects (single or array).
func applyFormatToOutput(output any, format any, inputs map[string]any, namespaces map[string]string) {
	if format == nil || output == nil {
		return
	}

	switch val := output.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			applyOutputFormat(val, format, inputs, namespaces)
		}
	case []map[string]any:
		for _, item := range val {
			if class, ok := item["class"].(string); ok && class == "File" {
				applyOutputFormat(item, format, inputs, namespaces)
			}
		}
	case []any:
		for _, item := range val {
			if itemMap, ok := item.(map[string]any); ok {
				if class, ok := itemMap["class"].(string); ok && class == "File" {
					applyOutputFormat(itemMap, format, inputs, namespaces)
				}
			}
		}
	}
}

// resolveNamespacePrefix resolves a namespace prefix to a full URI.
// e.g., "edam:format_2330" -> "http://edamontology.org/format_2330"
func resolveNamespacePrefix(s string, namespaces map[string]string) string {
	if namespaces == nil {
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
	if uri, ok := namespaces[prefix]; ok {
		return uri + s[idx+1:]
	}

	return s
}
