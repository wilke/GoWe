// Package stepinput provides shared step input resolution logic for CWL workflows.
// It implements the CWL semantics for resolving step inputs including:
// - Source resolution from workflow inputs or upstream step outputs
// - Default value fallback
// - loadContents for reading file contents
// - valueFrom expression evaluation
package stepinput

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/fileliteral"
)

// InputDef represents a step input definition with all CWL semantics.
type InputDef struct {
	ID           string   // Input parameter ID
	Sources      []string // Source references (workflow inputs or step/output)
	Default      any      // Default value if sources resolve to nil
	ValueFrom    string   // Expression to transform input
	LoadContents bool     // Read file contents before valueFrom
}

// Options configures step input resolution.
type Options struct {
	CWLDir        string   // Directory for resolving relative paths
	ExpressionLib []string // JavaScript library from InlineJavascriptRequirement
}

// ResolveInputs resolves step inputs following CWL semantics:
// 1. Resolve source(s) from workflow inputs or upstream step outputs
// 2. Apply default if value is nil
// 3. Apply loadContents to read file contents
// 4. Evaluate valueFrom expression
//
// Returns a map of input ID to resolved value.
func ResolveInputs(
	inputs []InputDef,
	workflowInputs map[string]any,
	stepOutputs map[string]map[string]any,
	opts Options,
) (map[string]any, error) {
	resolved := make(map[string]any, len(inputs))

	// First pass: resolve sources and defaults.
	for _, inp := range inputs {
		var value any
		if len(inp.Sources) == 1 {
			// Single source - value is the resolved source.
			value = ResolveSource(inp.Sources[0], workflowInputs, stepOutputs)
		} else if len(inp.Sources) > 1 {
			// Multiple sources (MultipleInputFeatureRequirement) - value is array of resolved sources.
			values := make([]any, len(inp.Sources))
			for i, src := range inp.Sources {
				values[i] = ResolveSource(src, workflowInputs, stepOutputs)
			}
			value = values
		}

		// Apply default if source resolved to nil.
		if value == nil && inp.Default != nil {
			value = ResolveDefaultValue(inp.Default, opts.CWLDir)
		}

		resolved[inp.ID] = value
	}

	// Second pass: apply loadContents before valueFrom.
	// This happens before valueFrom so expressions can access self.contents.
	for _, inp := range inputs {
		if inp.LoadContents {
			if val := resolved[inp.ID]; val != nil {
				resolved[inp.ID] = ApplyLoadContents(val, opts.CWLDir)
			}
		}
	}

	// Third pass: evaluate valueFrom expressions.
	// Per CWL spec, `inputs` provides the source-resolved values (before valueFrom transformation),
	// so all valueFrom expressions see the same snapshot of input values.
	if err := evaluateValueFromExpressions(inputs, resolved, workflowInputs, opts); err != nil {
		return nil, err
	}

	return resolved, nil
}

// ResolveSource resolves a source reference to its value.
// Handles both workflow input references ("input_id") and
// step output references ("step_id/output_id").
func ResolveSource(source string, workflowInputs map[string]any, stepOutputs map[string]map[string]any) any {
	if source == "" {
		return nil
	}

	// Check if it's a step output reference (step_id/output_id).
	if idx := strings.Index(source, "/"); idx >= 0 {
		stepID := source[:idx]
		outputID := source[idx+1:]
		if outputs, ok := stepOutputs[stepID]; ok {
			return outputs[outputID]
		}
		return nil
	}

	// Otherwise it's a workflow input reference.
	return workflowInputs[source]
}

// ResolveDefaultValue resolves a default value, handling File/Directory objects specially.
// File and Directory paths are resolved relative to the CWL file directory.
func ResolveDefaultValue(v any, cwlDir string) any {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				// Resolve File/Directory paths relative to CWL file.
				return resolveFileObject(val, cwlDir)
			}
		}
		// Recursively resolve nested maps.
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = ResolveDefaultValue(v, cwlDir)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = ResolveDefaultValue(item, cwlDir)
		}
		return resolved
	default:
		return v
	}
}

// resolveFileObject resolves paths in a File or Directory object relative to cwlDir.
// It also handles file literals (File objects with contents but no path/location).
func resolveFileObject(obj map[string]any, cwlDir string) map[string]any {
	result := make(map[string]any, len(obj))
	for k, v := range obj {
		result[k] = v
	}

	// Handle file literals: File objects with "contents" but no path/location.
	// These need to be materialized as actual files before execution.
	if _, err := fileliteral.MaterializeFileObject(result); err != nil {
		// Log error but continue - file literals are not critical.
		_ = err
	}

	// Resolve location or path.
	for _, key := range []string{"location", "path"} {
		if loc, ok := result[key].(string); ok && loc != "" {
			// Handle file:// URLs.
			path := loc
			if strings.HasPrefix(path, "file://") {
				path = strings.TrimPrefix(path, "file://")
			}
			// Make relative paths absolute.
			if !filepath.IsAbs(path) && cwlDir != "" {
				path = filepath.Join(cwlDir, path)
			}
			result[key] = path
			// Also set the other key for completeness.
			if key == "location" && result["path"] == nil {
				result["path"] = path
			} else if key == "path" && result["location"] == nil {
				result["location"] = "file://" + path
			}
		}
	}

	// Recursively resolve Directory listings (may contain file literals).
	if listing, ok := result["listing"].([]any); ok {
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				listing[i] = resolveFileObject(itemMap, cwlDir)
			}
		}
	}

	// Recursively resolve secondaryFiles.
	if sf, ok := result["secondaryFiles"].([]any); ok {
		resolved := make([]any, len(sf))
		for i, f := range sf {
			if fmap, ok := f.(map[string]any); ok {
				resolved[i] = resolveFileObject(fmap, cwlDir)
			} else {
				resolved[i] = f
			}
		}
		result["secondaryFiles"] = resolved
	}

	return result
}

// ApplyLoadContents reads the first 64 KiB of a file and adds it to the contents field.
// This implements CWL's loadContents feature for File objects.
func ApplyLoadContents(value any, cwlDir string) any {
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
			result[i] = ApplyLoadContents(item, cwlDir)
		}
		return result
	default:
		return value
	}
}

// evaluateValueFromExpressions evaluates valueFrom expressions on inputs.
func evaluateValueFromExpressions(
	inputs []InputDef,
	resolved map[string]any,
	workflowInputs map[string]any,
	opts Options,
) error {
	// Check if any inputs have valueFrom.
	hasValueFrom := false
	for _, inp := range inputs {
		if inp.ValueFrom != "" {
			hasValueFrom = true
			break
		}
	}
	if !hasValueFrom {
		return nil
	}

	// Create evaluator.
	evaluator := cwlexpr.NewEvaluator(opts.ExpressionLib)

	// Build the inputs context: workflow inputs as base, step inputs override.
	// This snapshot is used for ALL valueFrom evaluations (not updated between them).
	inputsCtx := make(map[string]any)
	for k, v := range workflowInputs {
		inputsCtx[k] = v
	}
	for k, v := range resolved {
		inputsCtx[k] = v
	}

	// Evaluate each valueFrom expression.
	for _, inp := range inputs {
		if inp.ValueFrom == "" {
			continue
		}
		self := resolved[inp.ID]
		ctx := cwlexpr.NewContext(inputsCtx).WithSelf(self)
		evaluated, err := evaluator.Evaluate(inp.ValueFrom, ctx)
		if err != nil {
			return fmt.Errorf("input %s valueFrom: %w", inp.ID, err)
		}
		resolved[inp.ID] = evaluated
		// Note: We intentionally do NOT update inputsCtx here.
		// Per CWL spec, `inputs` in valueFrom expressions should contain the
		// source-resolved values (before valueFrom), not transformed values.
	}
	return nil
}

// InputDefFromModel creates an InputDef from model.StepInput fields.
// This is a convenience function for converting from the model representation.
func InputDefFromModel(id string, sources []string, source string, defaultVal any, valueFrom string, loadContents bool) InputDef {
	// Prefer Sources array, fall back to splitting comma-separated Source.
	srcs := sources
	if len(srcs) == 0 && source != "" {
		srcs = strings.Split(source, ",")
	}
	return InputDef{
		ID:           id,
		Sources:      srcs,
		Default:      defaultVal,
		ValueFrom:    valueFrom,
		LoadContents: loadContents,
	}
}
