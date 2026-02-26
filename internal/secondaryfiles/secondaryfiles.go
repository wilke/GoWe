// Package secondaryfiles provides resolution and validation of CWL secondary files.
// This is used by both cwl-runner and the distributed execution engine.
package secondaryfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// ResolveForTool resolves secondary files for all tool inputs based on tool declarations.
// It discovers secondary files on disk based on patterns defined in the tool.
func ResolveForTool(tool *cwl.CommandLineTool, inputs map[string]any, cwlDir string) map[string]any {
	result := make(map[string]any)

	// Copy all inputs first.
	for k, v := range inputs {
		result[k] = v
	}

	// Resolve secondaryFiles for each input based on tool's input definitions.
	for inputID, inputDef := range tool.Inputs {
		val, exists := result[inputID]
		if !exists || val == nil {
			continue
		}

		// Handle secondaryFiles at the input level.
		if len(inputDef.SecondaryFiles) > 0 {
			result[inputID] = ResolveForValue(val, inputDef.SecondaryFiles, cwlDir)
			continue
		}

		// Handle record types with field-level secondaryFiles.
		if len(inputDef.RecordFields) > 0 {
			recordVal, ok := val.(map[string]any)
			if !ok {
				continue
			}

			// Create a copy to avoid modifying the original.
			resolvedRecord := make(map[string]any)
			for k, v := range recordVal {
				resolvedRecord[k] = v
			}

			// Resolve secondaryFiles for each field.
			for _, field := range inputDef.RecordFields {
				if len(field.SecondaryFiles) == 0 {
					continue
				}
				if fieldVal, exists := resolvedRecord[field.Name]; exists && fieldVal != nil {
					resolvedRecord[field.Name] = ResolveForValue(fieldVal, field.SecondaryFiles, cwlDir)
				}
			}
			result[inputID] = resolvedRecord
		}
	}

	return result
}

// ResolveForValue resolves secondary files for a File or array of Files.
// This can be used for both tool inputs and workflow inputs.
func ResolveForValue(val any, schemas []cwl.SecondaryFileSchema, cwlDir string) any {
	switch v := val.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			return resolveForFile(v, schemas, cwlDir)
		}
		return v

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = ResolveForValue(item, schemas, cwlDir)
		}
		return result

	default:
		return val
	}
}

// resolveForFile adds secondary files to a File object based on patterns.
func resolveForFile(fileObj map[string]any, schemas []cwl.SecondaryFileSchema, cwlDir string) map[string]any {
	// Create a copy to avoid modifying the original.
	result := make(map[string]any)
	for k, v := range fileObj {
		result[k] = v
	}

	// Get the file's path or location.
	var filePath string
	if p, ok := result["path"].(string); ok {
		filePath = p
	} else if loc, ok := result["location"].(string); ok {
		filePath = strings.TrimPrefix(loc, "file://")
	}
	if filePath == "" {
		return result
	}

	// Resolve relative paths.
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwlDir, filePath)
	}

	// Get existing secondary files (if any).
	var secondaryFiles []any
	if existing, ok := result["secondaryFiles"].([]any); ok {
		secondaryFiles = existing
	}

	// Add secondary files based on patterns.
	basename := filepath.Base(filePath)
	dir := filepath.Dir(filePath)

	for _, schema := range schemas {
		secFileName := ComputeSecondaryFileName(basename, schema.Pattern, result, nil)

		// Skip empty names (expression evaluation failed or returned nil).
		if secFileName == "" {
			continue
		}

		secPath := filepath.Join(dir, secFileName)

		// Check if the secondary file exists.
		if _, err := os.Stat(secPath); err != nil {
			// File doesn't exist - skip (validation will catch this later if required).
			continue
		}

		// Create the secondary file object.
		secFileObj := map[string]any{
			"class":    "File",
			"path":     secPath,
			"basename": secFileName,
			"location": "file://" + secPath,
		}

		// Add file metadata.
		if info, err := os.Stat(secPath); err == nil {
			secFileObj["size"] = info.Size()
		}

		secondaryFiles = append(secondaryFiles, secFileObj)
	}

	if len(secondaryFiles) > 0 {
		result["secondaryFiles"] = secondaryFiles
	}

	return result
}

// ValidateInput checks that required secondary files are present for a tool input.
// It handles both direct file inputs and record fields with secondaryFiles requirements.
func ValidateInput(inputID string, inputDef cwl.ToolInputParam, val any) error {
	// Check if input parameter has secondaryFiles requirements.
	if len(inputDef.SecondaryFiles) > 0 {
		if err := checkFileHasSecondaryFiles(inputID, val, inputDef.SecondaryFiles); err != nil {
			return err
		}
	}

	// Check if record fields have secondaryFiles requirements.
	if len(inputDef.RecordFields) > 0 {
		recordVal, ok := val.(map[string]any)
		if !ok {
			return nil // Not a record value, nothing to validate.
		}

		for _, field := range inputDef.RecordFields {
			if len(field.SecondaryFiles) == 0 {
				continue
			}

			fieldVal, exists := recordVal[field.Name]
			if !exists || fieldVal == nil {
				continue
			}

			fieldPath := inputID + "." + field.Name
			if err := checkFileHasSecondaryFiles(fieldPath, fieldVal, field.SecondaryFiles); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkFileHasSecondaryFiles validates that a File value has the required secondary files.
func checkFileHasSecondaryFiles(path string, val any, required []cwl.SecondaryFileSchema) error {
	switch v := val.(type) {
	case map[string]any:
		// Single File object.
		if class, ok := v["class"].(string); ok && class == "File" {
			return validateFileSecondaryFiles(path, v, required)
		}
		return nil

	case []any:
		// Array of Files.
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if err := checkFileHasSecondaryFiles(itemPath, item, required); err != nil {
				return err
			}
		}
		return nil

	default:
		return nil
	}
}

// validateFileSecondaryFiles checks that a single File object has the required secondary files.
func validateFileSecondaryFiles(path string, fileObj map[string]any, required []cwl.SecondaryFileSchema) error {
	// Get the list of secondary files attached to this file.
	existingSecondary := make(map[string]bool)
	if secFiles, ok := fileObj["secondaryFiles"].([]any); ok {
		for _, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				if loc, ok := sfMap["location"].(string); ok {
					existingSecondary[filepath.Base(loc)] = true
				} else if p, ok := sfMap["path"].(string); ok {
					existingSecondary[filepath.Base(p)] = true
				}
			}
		}
	}

	// Get the basename of the primary file.
	var basename string
	if b, ok := fileObj["basename"].(string); ok {
		basename = b
	} else if loc, ok := fileObj["location"].(string); ok {
		basename = filepath.Base(loc)
	} else if p, ok := fileObj["path"].(string); ok {
		basename = filepath.Base(p)
	}

	// Check each required secondary file.
	for _, schema := range required {
		// Skip if required is explicitly false.
		if req, ok := schema.Required.(bool); ok && !req {
			continue
		}

		// Compute the expected secondary file name.
		expectedName := ComputeSecondaryFileName(basename, schema.Pattern, fileObj, nil)

		// Skip empty names (expression evaluation failed or returned nil).
		if expectedName == "" {
			continue
		}

		if !existingSecondary[expectedName] {
			return fmt.Errorf("input %q: missing required secondary file %q (pattern: %s)", path, expectedName, schema.Pattern)
		}
	}

	return nil
}

// ComputeSecondaryFileName computes the secondary file name from a base name and pattern.
// If the pattern is a JavaScript expression, it evaluates it with 'self' set to the file object.
// inputs is optional and used for expressions that reference $(inputs.xxx).
func ComputeSecondaryFileName(basename, pattern string, fileObj map[string]any, inputs map[string]any) string {
	// Check if this is a JavaScript expression.
	if cwlexpr.IsExpression(pattern) {
		// Evaluate the expression with 'self' set to the file object.
		evaluator := cwlexpr.NewEvaluator(nil)
		ctx := cwlexpr.NewContext(inputs).WithSelf(fileObj)
		result, err := evaluator.Evaluate(pattern, ctx)
		if err != nil {
			// Fall back to treating it as a literal if evaluation fails.
			return basename + pattern
		}

		// Handle the result.
		switch v := result.(type) {
		case string:
			return v
		case map[string]any:
			// File object returned - extract the path.
			if p, ok := v["path"].(string); ok {
				return filepath.Base(p)
			}
			if bn, ok := v["basename"].(string); ok {
				return bn
			}
		case []any:
			// Array of results - take the first string.
			for _, item := range v {
				if s, ok := item.(string); ok {
					return s
				}
				if m, ok := item.(map[string]any); ok {
					if bn, ok := m["basename"].(string); ok {
						return bn
					}
				}
			}
		}
		// If we can't extract a name, return empty.
		return ""
	}

	// Handle caret pattern (replace extension).
	if strings.HasPrefix(pattern, "^") {
		// Count carets and remove that many extensions.
		carets := 0
		for strings.HasPrefix(pattern[carets:], "^") {
			carets++
		}
		suffix := pattern[carets:]

		// Remove extensions.
		name := basename
		for i := 0; i < carets; i++ {
			ext := filepath.Ext(name)
			if ext == "" {
				break
			}
			name = name[:len(name)-len(ext)]
		}
		return name + suffix
	}

	// Simple suffix pattern.
	return basename + pattern
}
