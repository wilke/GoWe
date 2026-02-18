package execution

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
func (e *Engine) collectOutputs(tool *cwl.CommandLineTool, workDir string, inputs map[string]any, exitCode int) (map[string]any, error) {
	outputs := make(map[string]any)

	// Check for cwl.output.json first.
	cwlOutputPath := filepath.Join(workDir, "cwl.output.json")
	if data, err := os.ReadFile(cwlOutputPath); err == nil {
		if err := json.Unmarshal(data, &outputs); err != nil {
			return nil, fmt.Errorf("parse cwl.output.json: %w", err)
		}
		processOutputObjects(outputs, workDir)
		return outputs, nil
	}

	// Collect outputs based on type and outputBinding.
	for outputID, output := range tool.Outputs {
		// Handle stdout type.
		if output.Type == "stdout" {
			stdoutFile := tool.Stdout
			if stdoutFile == "" {
				stdoutFile = "cwl.stdout.txt"
			} else if cwlexpr.IsExpression(stdoutFile) {
				ctx := cwlexpr.NewContext(inputs)
				evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
				if evaluated, err := evaluator.EvaluateString(stdoutFile, ctx); err == nil {
					stdoutFile = evaluated
				}
			}
			stdoutPath := filepath.Join(workDir, stdoutFile)
			if fileInfo, err := os.Stat(stdoutPath); err == nil && !fileInfo.IsDir() {
				fileObj, err := createFileObject(stdoutPath, false)
				if err != nil {
					return nil, fmt.Errorf("output %s: %w", outputID, err)
				}
				e.applyOutputFormat(fileObj, output.Format, inputs)
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle stderr type.
		if output.Type == "stderr" {
			stderrFile := tool.Stderr
			if stderrFile == "" {
				stderrFile = "cwl.stderr.txt"
			} else if cwlexpr.IsExpression(stderrFile) {
				ctx := cwlexpr.NewContext(inputs)
				evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
				if evaluated, err := evaluator.EvaluateString(stderrFile, ctx); err == nil {
					stderrFile = evaluated
				}
			}
			stderrPath := filepath.Join(workDir, stderrFile)
			if fileInfo, err := os.Stat(stderrPath); err == nil && !fileInfo.IsDir() {
				fileObj, err := createFileObject(stderrPath, false)
				if err != nil {
					return nil, fmt.Errorf("output %s: %w", outputID, err)
				}
				e.applyOutputFormat(fileObj, output.Format, inputs)
				outputs[outputID] = fileObj
				continue
			}
		}

		// Handle record type with field-level outputBindings.
		if output.Type == "record" && len(output.OutputRecordFields) > 0 {
			recordOutput, err := e.collectRecordOutput(output.OutputRecordFields, workDir, inputs, tool, exitCode)
			if err != nil {
				return nil, fmt.Errorf("output %s: %w", outputID, err)
			}
			outputs[outputID] = recordOutput
			continue
		}

		// Handle standard outputBinding.
		if output.OutputBinding == nil {
			outputs[outputID] = nil
			continue
		}

		collected, err := e.collectOutputBinding(output.OutputBinding, output.Type, workDir, inputs, tool, exitCode)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputID, err)
		}

		e.applyFormatToOutput(collected, output.Format, inputs)
		outputs[outputID] = collected
	}

	return outputs, nil
}

// collectRecordOutput collects output for a record type.
func (e *Engine) collectRecordOutput(fields []cwl.OutputRecordField, workDir string, inputs map[string]any, tool *cwl.CommandLineTool, exitCode int) (map[string]any, error) {
	record := make(map[string]any)

	for _, field := range fields {
		if field.OutputBinding == nil {
			record[field.Name] = nil
			continue
		}

		collected, err := e.collectOutputBinding(field.OutputBinding, field.Type, workDir, inputs, tool, exitCode)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.Name, err)
		}

		if len(field.SecondaryFiles) > 0 {
			collected = e.addSecondaryFilesToOutput(collected, field.SecondaryFiles, workDir)
		}

		record[field.Name] = collected
	}

	return record, nil
}

// collectOutputBinding collects files matching an output binding.
func (e *Engine) collectOutputBinding(binding *cwl.OutputBinding, outputType any, workDir string, inputs map[string]any, tool *cwl.CommandLineTool, exitCode int) (any, error) {
	// If there's an outputEval but no glob, evaluate it directly.
	if binding.Glob == nil {
		if binding.OutputEval != "" {
			ctx := cwlexpr.NewContext(inputs)
			ctx = ctx.WithSelf(nil)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = workDir
			runtime.ExitCode = exitCode
			ctx = ctx.WithRuntime(runtime)
			ctx = ctx.WithOutputEval()

			evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
			return evaluator.Evaluate(binding.OutputEval, ctx)
		}
		return nil, nil
	}

	// Evaluate glob patterns.
	patterns := e.getGlobPatterns(binding.Glob, inputs, tool, workDir)
	if len(patterns) == 0 {
		return nil, nil
	}

	expectDirectory := isDirectoryType(outputType)
	expectFile := isFileType(outputType)

	var collected []map[string]any
	for _, pattern := range patterns {
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

			// Type checking.
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
		runtime.OutDir = workDir
		runtime.ExitCode = exitCode
		ctx = ctx.WithRuntime(runtime)
		ctx = ctx.WithOutputEval()

		evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
		return evaluator.Evaluate(binding.OutputEval, ctx)
	}

	// Return single file or array based on type.
	isArrayType := isArrayOutputType(outputType)
	if isArrayType {
		return collected, nil
	}
	if len(collected) == 1 {
		return collected[0], nil
	}
	return collected, nil
}

// getGlobPatterns extracts glob patterns from the binding.
func (e *Engine) getGlobPatterns(glob any, inputs map[string]any, tool *cwl.CommandLineTool, workDir string) []string {
	switch g := glob.(type) {
	case string:
		if cwlexpr.IsExpression(g) {
			ctx := cwlexpr.NewContext(inputs)
			runtime := cwlexpr.DefaultRuntimeContext()
			runtime.OutDir = workDir
			ctx = ctx.WithRuntime(runtime)
			evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
			result, err := evaluator.Evaluate(g, ctx)
			if err != nil {
				return nil
			}
			return e.getGlobPatterns(result, inputs, tool, workDir)
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

// addSecondaryFilesToOutput adds secondary files to File objects.
func (e *Engine) addSecondaryFilesToOutput(output any, schemas []cwl.SecondaryFileSchema, workDir string) any {
	switch v := output.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			e.addSecondaryFiles(v, schemas, workDir)
		}
		return v
	case []map[string]any:
		for _, item := range v {
			if class, ok := item["class"].(string); ok && class == "File" {
				e.addSecondaryFiles(item, schemas, workDir)
			}
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = e.addSecondaryFilesToOutput(item, schemas, workDir)
		}
		return v
	default:
		return output
	}
}

// addSecondaryFiles adds secondary files to a File object.
func (e *Engine) addSecondaryFiles(fileObj map[string]any, schemas []cwl.SecondaryFileSchema, workDir string) {
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

		// Apply pattern to derive secondary file path.
		secondaryPath := path
		for strings.HasPrefix(pattern, "^") {
			ext := filepath.Ext(secondaryPath)
			if ext != "" {
				secondaryPath = strings.TrimSuffix(secondaryPath, ext)
			}
			pattern = strings.TrimPrefix(pattern, "^")
		}
		secondaryPath = secondaryPath + pattern

		if _, err := os.Stat(secondaryPath); err != nil {
			continue
		}

		secFileObj, err := createFileObject(secondaryPath, false)
		if err != nil {
			continue
		}
		secondaryFiles = append(secondaryFiles, secFileObj)
	}

	if len(secondaryFiles) > 0 {
		fileObj["secondaryFiles"] = secondaryFiles
	}
}

// applyOutputFormat adds the format field to a File object.
func (e *Engine) applyOutputFormat(fileObj map[string]any, format any, inputs map[string]any) {
	if format == nil {
		return
	}

	formatStr, ok := format.(string)
	if !ok {
		return
	}

	if cwlexpr.IsExpression(formatStr) {
		ctx := cwlexpr.NewContext(inputs)
		evaluator := cwlexpr.NewEvaluator(e.ExpressionLib)
		result, err := evaluator.Evaluate(formatStr, ctx)
		if err == nil {
			if resultStr, ok := result.(string); ok {
				formatStr = resultStr
			}
		}
	}

	resolvedFormat := e.resolveNamespacePrefix(formatStr)
	fileObj["format"] = resolvedFormat
}

// applyFormatToOutput applies format to collected output objects.
func (e *Engine) applyFormatToOutput(output any, format any, inputs map[string]any) {
	if format == nil || output == nil {
		return
	}

	switch val := output.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			e.applyOutputFormat(val, format, inputs)
		}
	case []map[string]any:
		for _, item := range val {
			if class, ok := item["class"].(string); ok && class == "File" {
				e.applyOutputFormat(item, format, inputs)
			}
		}
	case []any:
		for _, item := range val {
			if itemMap, ok := item.(map[string]any); ok {
				if class, ok := itemMap["class"].(string); ok && class == "File" {
					e.applyOutputFormat(itemMap, format, inputs)
				}
			}
		}
	}
}

// resolveNamespacePrefix resolves a namespace prefix to a full URI.
func (e *Engine) resolveNamespacePrefix(s string) string {
	if e.Namespaces == nil {
		return s
	}

	idx := strings.Index(s, ":")
	if idx <= 0 {
		return s
	}

	prefix := s[:idx]
	if prefix == "http" || prefix == "https" || prefix == "file" {
		return s
	}

	if uri, ok := e.Namespaces[prefix]; ok {
		return uri + s[idx+1:]
	}

	return s
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

	entries, err := os.ReadDir(path)
	if err != nil {
		return obj, nil
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

	obj["listing"] = listing
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

// processOutputObjects recursively processes File/Directory objects in outputs.
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
func processFileOutput(obj map[string]any, workDir string) map[string]any {
	if path, ok := obj["path"].(string); ok {
		if !filepath.IsAbs(path) {
			obj["path"] = filepath.Join(workDir, path)
		}
	}

	if loc, ok := obj["location"].(string); ok {
		if !filepath.IsAbs(loc) && !strings.Contains(loc, "://") {
			obj["location"] = filepath.Join(workDir, loc)
		}
	}

	if _, hasLoc := obj["location"]; !hasLoc {
		if path, ok := obj["path"].(string); ok {
			obj["location"] = path
		}
	}

	path := ""
	if p, ok := obj["path"].(string); ok {
		path = p
	} else if loc, ok := obj["location"].(string); ok {
		path = loc
	}

	if path == "" {
		return obj
	}

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
	if path, ok := obj["path"].(string); ok {
		if !filepath.IsAbs(path) {
			obj["path"] = filepath.Join(workDir, path)
		}
	}

	if loc, ok := obj["location"].(string); ok {
		if !filepath.IsAbs(loc) && !strings.Contains(loc, "://") {
			obj["location"] = filepath.Join(workDir, loc)
		}
	}

	if _, hasLoc := obj["location"]; !hasLoc {
		if path, ok := obj["path"].(string); ok {
			obj["location"] = path
		}
	}

	return obj
}

// Type checking helpers.

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
