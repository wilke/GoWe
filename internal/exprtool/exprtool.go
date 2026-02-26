// Package exprtool provides shared CWL ExpressionTool execution logic.
// ExpressionTools evaluate JavaScript expressions to produce outputs.
// Unlike CommandLineTools, they don't run external commands.
package exprtool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// ExecuteOptions configures ExpressionTool execution.
type ExecuteOptions struct {
	// ExpressionLib contains JavaScript library code from InlineJavascriptRequirement.
	ExpressionLib []string

	// CWLDir is the directory containing the CWL file, used for resolving paths.
	CWLDir string
}

// Execute evaluates a CWL ExpressionTool and returns its outputs.
// ExpressionTools are pure JavaScript computations that don't run external commands.
func Execute(tool *cwl.ExpressionTool, inputs map[string]any, opts ExecuteOptions) (map[string]any, error) {
	// Apply loadContents for inputs that have it enabled.
	processedInputs := make(map[string]any)
	for inputID, val := range inputs {
		processedInputs[inputID] = val
	}
	for inputID, inputDef := range tool.Inputs {
		if inputDef.LoadContents {
			if val, exists := processedInputs[inputID]; exists && val != nil {
				processedInputs[inputID] = applyLoadContents(val, opts.CWLDir)
			}
		}
	}

	// Create expression context with processed inputs.
	ctx := cwlexpr.NewContext(processedInputs)
	evaluator := cwlexpr.NewEvaluator(opts.ExpressionLib)

	// Evaluate the expression.
	result, err := evaluator.Evaluate(tool.Expression, ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate expression: %w", err)
	}

	// The expression should return an object with output field names.
	outputs, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expression did not return an object, got %T", result)
	}

	return outputs, nil
}

// applyLoadContents reads the first 64 KiB of a file and adds it to the contents field.
// This implements CWL's loadContents feature for File objects.
func applyLoadContents(value any, cwlDir string) any {
	switch v := value.(type) {
	case map[string]any:
		// Check if this is a File object.
		if class, ok := v["class"].(string); ok && class == "File" {
			// Get the file path.
			path := ""
			if p, ok := v["path"].(string); ok {
				path = p
			} else if p, ok := v["location"].(string); ok {
				path = p
			}
			if path == "" {
				return value
			}

			// Handle file:// URLs.
			if strings.HasPrefix(path, "file://") {
				path = strings.TrimPrefix(path, "file://")
			}

			// Make path absolute if needed.
			if !filepath.IsAbs(path) && cwlDir != "" {
				path = filepath.Join(cwlDir, path)
			}

			// Read up to 64 KiB of the file.
			const maxSize = 64 * 1024
			data, err := os.ReadFile(path)
			if err != nil {
				return value // Return unchanged if we can't read.
			}
			if len(data) > maxSize {
				data = data[:maxSize]
			}

			// Create a new map with contents field.
			result := make(map[string]any, len(v)+1)
			for k, val := range v {
				result[k] = val
			}
			result["contents"] = string(data)
			return result
		}
		return value
	case []any:
		// Handle arrays of files.
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = applyLoadContents(item, cwlDir)
		}
		return result
	default:
		return value
	}
}
