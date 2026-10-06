// Package validate provides CWL input validation utilities.
// This package is used by both cwl-runner and the distributed execution engine.
package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// ErrInputValidation is a sentinel error for input validation failures.
// Wrap with %w so callers can use errors.Is to distinguish validation
// errors from other populate failures (e.g., missing tool, parse errors).
var ErrInputValidation = errors.New("input validation")

// ApplyRecordFieldDefaults fills missing record fields that declare a
// default. It mutates records in place (single record or array of records)
// and leaves unknown/absent inputs alone. Returns the possibly-updated inputs.
func ApplyRecordFieldDefaults(tool *cwl.CommandLineTool, inputs map[string]any) map[string]any {
	for inputID, inputDef := range tool.Inputs {
		if len(inputDef.RecordFields) == 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		switch v := value.(type) {
		case map[string]any:
			applyDefaults(v, inputDef.RecordFields)
		case []any:
			for _, item := range v {
				if rec, ok := item.(map[string]any); ok {
					applyDefaults(rec, inputDef.RecordFields)
				}
			}
		}
	}
	return inputs
}

// applyDefaults fills missing fields in a single record with their defaults.
func applyDefaults(rec map[string]any, fields []cwl.RecordField) {
	for _, rf := range fields {
		if rf.Default == nil {
			continue
		}
		if _, exists := rec[rf.Name]; !exists {
			rec[rf.Name] = rf.Default
		}
	}
}

// ValidateRecordShape checks that record-typed inputs have the correct
// shape: a "record:x?" input must be a single object (not an array), and
// a "record:x[]?" input must be an array (not a bare object). Returns a
// descriptive error on mismatch.
func ValidateRecordShape(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		if len(inputDef.RecordFields) == 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		typeStr := inputDef.Type
		isArray := strings.Contains(typeStr, "[]")

		// Field names are quoted into the error so the caller (usually an
		// LLM populating the inputs) can correct itself without another
		// round trip to GET /workflows/:id/inputs.
		fieldNames := make([]string, 0, len(inputDef.RecordFields))
		for _, f := range inputDef.RecordFields {
			fieldNames = append(fieldNames, f.Name)
		}
		shape := fmt.Sprintf("a record with fields %v", fieldNames)
		if isArray {
			shape = fmt.Sprintf("an array of records with fields %v", fieldNames)
		}

		switch v := value.(type) {
		case []any:
			if !isArray {
				return fmt.Errorf(
					"%s expects %s, got an array (%w)",
					inputID, shape, ErrInputValidation,
				)
			}
			// Every element must itself be a record. Checking only the
			// container let `["/path/to/file"]` through for a record[] input,
			// which reached BV-BRC and died in Perl preflight with
			// "Can't use string as a HASH ref" — 11 Gene Tree and 11 MSA SNP
			// failures before this was caught (2026-10-05).
			for i, elem := range v {
				if _, ok := elem.(map[string]any); !ok {
					return fmt.Errorf(
						"%s[%d] expects %s, got %T (%w)",
						inputID, i, shape, elem, ErrInputValidation,
					)
				}
			}
		case map[string]any:
			if isArray {
				return fmt.Errorf(
					"%s expects %s, got a single object (%w)",
					inputID, shape, ErrInputValidation,
				)
			}
		default:
			// A scalar for a record input matched neither case above and
			// fell through silently. This is the MSA SNP failure mode: a
			// bare workspace path where a record was declared.
			return fmt.Errorf(
				"%s expects %s, got %T (%w)",
				inputID, shape, value, ErrInputValidation,
			)
		}
	}
	return nil
}

// ToolInputs validates that inputs match the tool's input schema.
// Returns an error if required inputs are missing, null is provided for
// non-optional types, or record-typed inputs contain unknown field names.
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

	// Validate record shape (array vs single object).
	if err := ValidateRecordShape(tool, inputs); err != nil {
		return err
	}

	// Validate record field names (catches wrong field names like "srr_id"
	// instead of "srr_accession" before they reach downstream executors).
	if err := ValidateRecordFields(tool, inputs); err != nil {
		return err
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

// ValidateRecordFields checks that record-typed inputs contain only field names
// declared in the CWL schema. This catches cases where an API caller (e.g., an
// LLM) uses a wrong field name (like "srr_id" instead of "srr_accession"),
// which would otherwise pass silently through to the downstream executor and
// cause a cryptic runtime error.
func ValidateRecordFields(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		if len(inputDef.RecordFields) == 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		// Build the set of valid field names for this record type.
		validFields := make(map[string]bool, len(inputDef.RecordFields))
		for _, rf := range inputDef.RecordFields {
			validFields[rf.Name] = true
		}

		// The input may be a single record (map) or an array of records.
		switch v := value.(type) {
		case map[string]any:
			if err := checkRecordKeys(inputID, v, validFields); err != nil {
				return err
			}
		case []any:
			for i, item := range v {
				rec, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if err := checkRecordKeys(fmt.Sprintf("%s[%d]", inputID, i), rec, validFields); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkRecordKeys reports an error if any key in rec is not in validFields.
func checkRecordKeys(context string, rec map[string]any, validFields map[string]bool) error {
	for key := range rec {
		if !validFields[key] {
			valid := make([]string, 0, len(validFields))
			for f := range validFields {
				valid = append(valid, f)
			}
			return fmt.Errorf(
				"unknown field %q in record input %s (valid fields: %s): %w",
				key, context, strings.Join(valid, ", "), ErrInputValidation,
			)
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

// ValidateEnumValues checks that any enum-typed input carries one of its
// permitted values.
//
// Enum violations were previously invisible to GoWe: the submission was
// accepted, reached BV-BRC, and failed minutes later inside the app. Observed
// cases, all of which this catches at submit time:
//
//	Docking   ligand_library_type = "named_library"
//	          -> "Unknown ligand library type selected named_library"
//	BLAST     input_source = "id_list" where a feature group was meant
//	GeneTree  sequences[].type = "fasta_file", not one of the declared values
//
// Handles a scalar value and an array of scalars (for a multi-valued enum).
// Non-string values are left alone — type checking is not this function's job.
// An input with no declared symbols is skipped, so this is inert for specs
// that declare none.
func ValidateEnumValues(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		if len(inputDef.Symbols) == 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		allowed := make(map[string]bool, len(inputDef.Symbols))
		for _, sym := range inputDef.Symbols {
			allowed[sym] = true
		}

		switch v := value.(type) {
		case string:
			if !allowed[v] {
				return enumError(inputID, "", v, inputDef.Symbols)
			}
		case []any:
			for i, elem := range v {
				sv, ok := elem.(string)
				if !ok {
					continue
				}
				if !allowed[sv] {
					return enumError(inputID, fmt.Sprintf("[%d]", i), sv, inputDef.Symbols)
				}
			}
		}
	}
	return nil
}

// enumError names the offending value and lists the permitted ones, so the
// caller (usually an LLM populating inputs) can correct itself without
// re-reading the workflow schema.
func enumError(inputID, index, got string, symbols []string) error {
	return fmt.Errorf(
		"%s%s: %q is not a permitted value; expected one of %v (%w)",
		inputID, index, got, symbols, ErrInputValidation,
	)
}
