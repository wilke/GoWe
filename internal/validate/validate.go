// Package validate provides CWL input validation utilities.
// This package is used by both cwl-runner and the distributed execution engine.
package validate

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ToolInputs validates that inputs match the tool's input schema: required/
// null checks (unchanged in every mode) plus, controlled by mode, a
// TypeSchema-level value check (#273) — off skips the new check entirely,
// warn logs (never the offending values) and continues, enforce returns an
// error wrapping ErrInputValidation. logger may be nil (defaults to
// slog.Default()).
func ToolInputs(tool *cwl.CommandLineTool, inputs map[string]any, mode Mode, logger *slog.Logger) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]

		// Check if input is optional (type ends with ? or is a union with null).
		isOptional := IsOptionalType(inputDef.Type)

		// Check for missing required inputs.
		if !exists {
			if inputDef.Default == nil && !isOptional {
				return fmt.Errorf("missing required input: %s", inputID)
			}
			continue
		}

		// Check for null values on non-optional inputs.
		// Exception: type "Any" with a default value - null means "use the default".
		if value == nil && !isOptional {
			if inputDef.Type == "Any" && inputDef.Default != nil {
				continue // null is allowed for Any with default - it will use the default.
			}
			return fmt.Errorf("null is not valid for non-optional input: %s (type: %s)", inputID, inputDef.Type)
		}
	}
	return validateToolLevelTypes(tool.Inputs, inputs, mode, logger, tool.ID)
}

// ExpressionToolInputs validates that inputs match the ExpressionTool's input
// schema: required/null checks (unchanged in every mode) plus, controlled by
// mode, a TypeSchema-level value check (#273) — see ToolInputs. Callers
// should pass inputs AFTER input defaults have been merged (see
// exprtool.MergeDefaults) so a null-with-default value is not mistakenly
// flagged.
func ExpressionToolInputs(tool *cwl.ExpressionTool, inputs map[string]any, mode Mode, logger *slog.Logger) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]

		// Check if input is optional (type ends with ? or is a union with null).
		isOptional := IsOptionalType(inputDef.Type)

		// Check for missing required inputs.
		if !exists {
			if inputDef.Default == nil && !isOptional {
				return fmt.Errorf("missing required input: %s", inputID)
			}
			continue
		}

		// Check for null values on non-optional inputs.
		// Exception: type "Any" with a default value - null means "use the default".
		if value == nil && !isOptional {
			if inputDef.Type == "Any" && inputDef.Default != nil {
				continue // null is allowed for Any with default - it will use the default.
			}
			return fmt.Errorf("null is not valid for non-optional input: %s (type: %s)", inputID, inputDef.Type)
		}
	}
	return validateToolLevelTypes(tool.Inputs, inputs, mode, logger, tool.ID)
}

// validateToolLevelTypes runs the #273 TypeSchema-level check shared by
// ToolInputs and ExpressionToolInputs. ToolLevel redaction (Options{ToolLevel:
// true}) means no offending value is ever included in a message, regardless
// of mode.
func validateToolLevelTypes(toolInputs map[string]cwl.ToolInputParam, inputs map[string]any, mode Mode, logger *slog.Logger, toolID string) error {
	effective := mode.Effective()
	if effective == ModeOff || len(toolInputs) == 0 {
		return nil
	}
	params := make(map[string]ParamSpec, len(toolInputs))
	for id, in := range toolInputs {
		params[id] = ParamSpec{TypeSchema: in.TypeSchema, Default: in.Default, HasDefault: in.Default != nil}
	}
	errs := ValidateInputs(params, inputs, Options{ToolLevel: true})
	if len(errs) == 0 {
		return nil
	}
	summary := SummarizeErrors(errs)
	if effective == ModeEnforce {
		return fmt.Errorf("%w: %s", ErrInputValidation, summary)
	}
	if logger == nil {
		logger = slog.Default()
	}
	fields := make([]string, len(errs))
	for i, e := range errs {
		fields[i] = e.Field
	}
	logger.Warn("tool-level input validation failed", "tool", toolID, "fields", fields)
	return nil
}

// SummarizeErrors renders a one-line, human-readable summary of field
// errors for a CLI exit message or an API error's top-level Message: the
// first error as `input "<id>"<path>: <message>`, plus " (+N more errors)"
// when there is more than one. Returns "" for an empty slice.
func SummarizeErrors(errs []model.FieldError) string {
	if len(errs) == 0 {
		return ""
	}
	first := errs[0]
	msg := fmt.Sprintf("input %q%s: %s", first.Field, first.Path, first.Message)
	if len(errs) > 1 {
		msg += fmt.Sprintf(" (+%d more errors)", len(errs)-1)
	}
	return msg
}

// IsOptionalType checks if a CWL type is optional (can be null).
// Types ending with ? or types that are unions including null are optional.
func IsOptionalType(t string) bool {
	if t == "" {
		return false
	}
	// Type ending with ? is optional.
	if strings.HasSuffix(t, "?") {
		return true
	}
	// "null" type itself is optional.
	if t == "null" {
		return true
	}
	return false
}

// ValidateFileInputs checks that all File/Directory inputs have a path or location.
// Returns an error if any File/Directory object is missing both fields, which would
// cause the command builder to produce invalid arguments like "map[class:File]".
func ValidateFileInputs(inputs map[string]any) error {
	for inputID, value := range inputs {
		if err := checkFilePathsRecursive(value, inputID); err != nil {
			return err
		}
	}
	return nil
}

func checkFilePathsRecursive(value any, fieldName string) error {
	switch v := value.(type) {
	case map[string]any:
		class, _ := v["class"].(string)
		if class == "File" || class == "Directory" {
			path, _ := v["path"].(string)
			loc, _ := v["location"].(string)
			_, hasContents := v["contents"]
			_, hasListing := v["listing"]
			// Literal files (contents) and literal directories (listing with no
			// path/location) are valid — they get materialized during IWDR staging.
			isLiteral := hasContents || (class == "Directory" && hasListing)
			if path == "" && loc == "" && !isLiteral {
				return fmt.Errorf("%s input %q has no path or location", class, fieldName)
			}
		}
		for k, item := range v {
			if err := checkFilePathsRecursive(item, fieldName+"."+k); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := checkFilePathsRecursive(item, fmt.Sprintf("%s[%d]", fieldName, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateFileFormat checks if File inputs have the required format.
// Returns an error if a file's format doesn't match the required format.
func ValidateFileFormat(tool *cwl.CommandLineTool, inputs map[string]any, namespaces map[string]string) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		// Check format on the input definition.
		if inputDef.Format != nil {
			if err := validateValueFormat(value, inputDef.Format, inputID, namespaces); err != nil {
				return err
			}
		}

		// Check format on record fields.
		if len(inputDef.RecordFields) > 0 {
			recordValue, ok := value.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range inputDef.RecordFields {
				if field.Format == nil {
					continue
				}
				fieldValue, exists := recordValue[field.Name]
				if !exists || fieldValue == nil {
					continue
				}
				if err := validateValueFormat(fieldValue, field.Format, inputID+"."+field.Name, namespaces); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateValueFormat checks if a value (File or array of Files) matches the required format.
func validateValueFormat(value any, requiredFormat any, fieldName string, namespaces map[string]string) error {
	switch v := value.(type) {
	case map[string]any:
		// Single File object.
		if class, ok := v["class"].(string); ok && class == "File" {
			return checkFileFormat(v, requiredFormat, fieldName, namespaces)
		}
	case []any:
		// Array of Files.
		for i, item := range v {
			if itemMap, ok := item.(map[string]any); ok {
				if class, ok := itemMap["class"].(string); ok && class == "File" {
					if err := checkFileFormat(itemMap, requiredFormat, fmt.Sprintf("%s[%d]", fieldName, i), namespaces); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// checkFileFormat checks if a single File object's format matches the required format.
// Note: Full ontology-based format checking (subclassOf/equivalentClass) is not yet implemented.
func checkFileFormat(fileObj map[string]any, requiredFormat any, fieldName string, namespaces map[string]string) error {
	fileFormat, hasFormat := fileObj["format"].(string)
	requiredFormatStr, ok := requiredFormat.(string)
	if !ok {
		return nil // Can't validate non-string format requirements
	}

	// Resolve namespace prefixes.
	resolvedRequired := resolveNamespacePrefix(requiredFormatStr, namespaces)

	if !hasFormat {
		return fmt.Errorf("file format mismatch for %s: expected '%s' but file has no format", fieldName, resolvedRequired)
	}

	resolvedFile := resolveNamespacePrefix(fileFormat, namespaces)

	// Exact match - always valid.
	if resolvedFile == resolvedRequired {
		return nil
	}

	// Check if formats are from an ontology namespace (EDAM or similar).
	// If so, allow the mismatch as it might be a valid subclass relationship.
	// Full ontology validation would require loading and parsing OWL files.
	if isOntologyFormat(resolvedFile) && isOntologyFormat(resolvedRequired) {
		// Both are ontology formats - allow subclass relationships.
		return nil
	}

	// For non-ontology formats, require exact match.
	return fmt.Errorf("file format mismatch for %s: expected '%s' but got '%s'", fieldName, resolvedRequired, resolvedFile)
}

// isOntologyFormat checks if a format URI is from a known ontology namespace.
func isOntologyFormat(format string) bool {
	// Known ontology prefixes.
	ontologyPrefixes := []string{
		"http://edamontology.org/",
		"https://edamontology.org/",
		"http://purl.obolibrary.org/",
		"https://purl.obolibrary.org/",
		"http://galaxyproject.org/formats/",
		"https://galaxyproject.org/formats/",
	}
	for _, prefix := range ontologyPrefixes {
		if strings.HasPrefix(format, prefix) {
			return true
		}
	}
	return false
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

	prefix := s[:idx]
	// Skip known URI schemes.
	if prefix == "http" || prefix == "https" || prefix == "file" {
		return s
	}

	// Look up prefix in namespaces.
	if uri, ok := namespaces[prefix]; ok {
		return uri + s[idx+1:]
	}

	return s
}
