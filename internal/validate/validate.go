// Package validate provides CWL input validation utilities.
// This package is used by both cwl-runner and the distributed execution engine.
package validate

import (
	"fmt"
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// ToolInputs validates that inputs match the tool's input schema.
// Returns an error if required inputs are missing or null is provided for non-optional types.
func ToolInputs(tool *cwl.CommandLineTool, inputs map[string]any) error {
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
	return nil
}

// ExpressionToolInputs validates that inputs match the ExpressionTool's input schema.
// Returns an error if required inputs are missing or null is provided for non-optional types.
func ExpressionToolInputs(tool *cwl.ExpressionTool, inputs map[string]any) error {
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
	return nil
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
