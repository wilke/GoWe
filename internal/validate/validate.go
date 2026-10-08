// Package validate provides CWL input validation utilities.
// This package is used by both cwl-runner and the distributed execution engine.
package validate

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
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
			// "Can't use string as a HASH ref" — 11 Gene Tree failures before
			// this was caught (2026-10-05).
			//
			// This comment previously also credited 11 MSA SNP failures.
			// Wrong: those passed `feature_groups` as a bare string on a
			// plain string[]? input, which is not a record at all and is
			// caught by ValidateArrayShape instead.
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
			sort.Strings(valid)
			return fmt.Errorf(
				"unknown field %q in record input %s (valid fields: %s): %w",
				key, context, strings.Join(valid, ", "), ErrInputValidation,
			)
		}
	}
	return nil
}

// ValidateRecordRequiredFields checks that each record carries the fields its
// schema declares as required.
//
// ValidateRecordFields catches a field that should not be there; this catches
// one that should. Nothing checked for a MISSING required field until
// 2026-10-07, so a half-built record validated and then failed inside the
// BV-BRC app. Proven on Gene Tree, whose own CWL marks sequences[].filename
// required:
//
//	{"sequences": [{"type": "feature_group"}]}  ->  accepted, no filename
//
// A field counts as required when its type is not nullable (no "?" suffix) and
// it declares no default — the same rule GET /inputs reports as
// `required: true`. Call this AFTER ApplyRecordFieldDefaults, so a field with
// a declared default has already been filled and is not reported missing.
//
// A nil or non-record value is left to ValidateRecordShape, and an absent
// input to ToolInputs; this function only inspects records that are present.
func ValidateRecordRequiredFields(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		if len(inputDef.RecordFields) == 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}

		var required []string
		for _, rf := range inputDef.RecordFields {
			if !strings.HasSuffix(rf.Type, "?") && rf.Default == nil {
				required = append(required, rf.Name)
			}
		}
		if len(required) == 0 {
			continue
		}

		switch v := value.(type) {
		case map[string]any:
			if err := checkRequiredKeys(inputID, v, required); err != nil {
				return err
			}
		case []any:
			for i, item := range v {
				rec, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if err := checkRequiredKeys(fmt.Sprintf("%s[%d]", inputID, i), rec, required); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkRequiredKeys reports the first required field absent from one record.
// A present-but-null value counts as absent: the app sees nothing either way.
func checkRequiredKeys(context string, rec map[string]any, required []string) error {
	for _, name := range required {
		v, ok := rec[name]
		if !ok || v == nil {
			return fmt.Errorf(
				"record input %s is missing required field %q: %w",
				context, name, ErrInputValidation,
			)
		}
	}
	return nil
}

// InputTypeWarnings reports values whose kind does not match the declared
// type. It REPORTS, it does not reject -- and that is a deliberate, measured
// decision rather than caution.
//
// Nothing enforced declared types until 2026-10-08. Probed against the live
// service, all of these were accepted:
//
//	srr_libs[].condition = "Cd0"        declared int?   (a LABEL, not an index)
//	srr_libs[].condition = ["a","b"]    declared int?
//	bootstrap            = "not-a-num"  declared int?
//
// A strict version of this check was written and then replayed against every
// COMPLETED submission BEFORE being wired in. It refused 22 of them:
//
//	14  Metagenomic Read Mapping  srr_ids   = ["ERR5260468"]   declared string?
//	 5  GenomeAssembly            genome_size = "5M"           declared int
//	 2  RNASeq                    contrasts = []               declared string?
//	 1  GenomeAnnotation          contigs   an object          declared string?
//
// Every one of those ran to completion on BV-BRC. So the app layer is loosely
// typed by design -- "5M" is a human-readable genome size, and several of our
// CWLs simply under-declare -- and a type gate would refuse payloads that
// demonstrably work. The declared type is not a contract BV-BRC honours.
//
// Reporting still has value: it is how the three CWL under-declarations above
// were found, and a wrong `condition` is invisible otherwise. Warnings surface
// in the dry-run report, where an agent or a developer can act on them without
// a submission being blocked.
//
// If a future case justifies rejecting a specific field, reject THAT field --
// do not promote this to a gate wholesale. The replay is the test that would
// catch it: internal/validate/history_replay_test.go.
func InputTypeWarnings(tool *cwl.CommandLineTool, inputs map[string]any) []string {
	var warnings []string
	for inputID, inputDef := range tool.Inputs {
		base := strings.TrimSuffix(inputDef.Type, "?")
		if strings.HasSuffix(base, "[]") || strings.HasPrefix(base, "record:") ||
			len(inputDef.RecordFields) > 0 {
			continue // ValidateArrayShape / ValidateRecordShape own these.
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}
		if err := checkScalarKind(inputID, base, value); err != nil {
			warnings = append(warnings, err.Error())
		}
	}

	// Record fields carry declared types too, and condition -- the field that
	// motivated this -- is one of them.
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
			warnings = append(warnings, checkRecordKinds(inputID, v, inputDef.RecordFields)...)
		case []any:
			for i, item := range v {
				rec, ok := item.(map[string]any)
				if !ok {
					continue
				}
				path := fmt.Sprintf("%s[%d]", inputID, i)
				warnings = append(warnings, checkRecordKinds(path, rec, inputDef.RecordFields)...)
			}
		}
	}
	return warnings
}

func checkRecordKinds(path string, rec map[string]any, fields []cwl.RecordField) []string {
	var warnings []string
	for _, rf := range fields {
		base := strings.TrimSuffix(rf.Type, "?")
		if strings.HasSuffix(base, "[]") {
			continue
		}
		v, ok := rec[rf.Name]
		if !ok || v == nil {
			continue
		}
		if err := checkScalarKind(path+"."+rf.Name, base, v); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

// checkScalarKind reports whether one value can be the declared scalar type.
func checkScalarKind(path, declared string, value any) error {
	switch value.(type) {
	case []any:
		return fmt.Errorf("%s expects %s, got a list (%w)",
			path, declared, ErrInputValidation)
	case map[string]any:
		return fmt.Errorf("%s expects %s, got an object (%w)",
			path, declared, ErrInputValidation)
	}

	switch declared {
	case "int", "long":
		switch v := value.(type) {
		case float64:
			if v != math.Trunc(v) {
				return fmt.Errorf("%s expects a whole number (%s), got %v (%w)",
					path, declared, v, ErrInputValidation)
			}
		case string:
			if _, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
				return fmt.Errorf(
					"%s expects %s, got the string %q (%w)",
					path, declared, v, ErrInputValidation)
			}
		case bool:
			return fmt.Errorf("%s expects %s, got a boolean (%w)",
				path, declared, ErrInputValidation)
		}
	case "float", "double":
		switch v := value.(type) {
		case float64:
		case string:
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
				return fmt.Errorf("%s expects %s, got the string %q (%w)",
					path, declared, v, ErrInputValidation)
			}
		case bool:
			return fmt.Errorf("%s expects %s, got a boolean (%w)",
				path, declared, ErrInputValidation)
		}
	case "boolean":
		switch v := value.(type) {
		case bool:
		case string:
			if _, err := strconv.ParseBool(strings.TrimSpace(v)); err != nil {
				return fmt.Errorf("%s expects a boolean, got the string %q (%w)",
					path, v, ErrInputValidation)
			}
		case float64:
			return fmt.Errorf("%s expects a boolean, got the number %v (%w)",
				path, v, ErrInputValidation)
		}
	}
	// string, File, Any and anything unrecognised accept any scalar: a JSON
	// number for a string input is a coercion BV-BRC performs happily.
	return nil
}

// ValidateArrayShape rejects a scalar passed where a plain array is declared.
//
// ValidateRecordShape covers arrays OF RECORDS; this covers the plain ones
// (string[], int[], …), which nothing checked until 2026-10-07. The catalog
// declares 35 such inputs across 19 workflows, among them the ones agents
// touch most: srr_ids, genome_ids, genome_groups, feature_groups.
//
// The evidence that this is always an error, never a tolerated shorthand, is
// unanimous in the submission history:
//
//	COMPLETED   0 of 81 array-valued inputs passed a scalar
//	FAILED     15 of 32 did
//
// All 11 MSA SNP failures are this bug — `feature_groups` as a bare string —
// and the single MSA SNP success passed a one-element list. BV-BRC's Perl
// reads these as array refs, so a scalar is a type error there, not a
// convenience.
//
// Rejects rather than silently wrapping the value in a list: coercing would
// hide the caller's mistake, and an LLM that gets a named error corrects
// itself, while one whose payload is quietly fixed never learns.
func ValidateArrayShape(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		baseType := strings.TrimSuffix(inputDef.Type, "?")
		if !strings.HasSuffix(baseType, "[]") {
			continue
		}
		// Arrays of records belong to ValidateRecordShape, which reports a
		// more specific message naming the expected fields.
		if strings.HasPrefix(baseType, "record:") || len(inputDef.RecordFields) > 0 {
			continue
		}
		value, exists := inputs[inputID]
		if !exists || value == nil {
			continue
		}
		switch value.(type) {
		case []any:
			// Correct shape. Element types are ToolInputs' business.
		case map[string]any:
			return fmt.Errorf(
				"%s expects an array (%s), got an object (%w)",
				inputID, inputDef.Type, ErrInputValidation,
			)
		default:
			return fmt.Errorf(
				"%s expects an array (%s), got a single %T — wrap it in a list, "+
					"e.g. [%v] (%w)",
				inputID, inputDef.Type, value, formatScalar(value), ErrInputValidation,
			)
		}
	}
	return nil
}

// formatScalar renders a scalar the way it should appear inside the suggested
// list, so the error text can be copied verbatim: quoted for a string, bare
// otherwise.
func formatScalar(v any) string {
	if sv, ok := v.(string); ok {
		return fmt.Sprintf("%q", sv)
	}
	return fmt.Sprintf("%v", v)
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
// Handles a scalar value, an array of scalars (for a multi-valued enum), and
// enum-typed fields INSIDE a record — a single record or an array of them.
// Record fields were invisible here until 2026-10-07: this walked only
// tool.Inputs, so 47 of the catalog's 298 declared values were unchecked, the
// GeneTree sequences[].type case above among them. Observed consequence: an
// agent asked about an *aligned* FASTA set type to "feature_dna_fasta", a
// declared-but-wrong value; the submission validated, reached BV-BRC and
// failed in the app.
//
// Non-string values are left alone — type checking is not this function's job.
// An input or field with no declared symbols is skipped, so this stays inert
// for specs that declare none.
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

	// Enum-typed fields inside a record. Kept in a second pass rather than
	// folded into the loop above because an input has either Symbols or
	// RecordFields, never both, and the two walks share nothing.
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
			if err := checkRecordEnums(inputID, v, inputDef.RecordFields); err != nil {
				return err
			}
		case []any:
			for i, item := range v {
				rec, ok := item.(map[string]any)
				if !ok {
					continue
				}
				path := fmt.Sprintf("%s[%d]", inputID, i)
				if err := checkRecordEnums(path, rec, inputDef.RecordFields); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkRecordEnums validates one record's enum-typed fields against their
// declared symbols. Fields with no symbols, absent fields and non-string
// values are skipped, matching the top-level behaviour.
func checkRecordEnums(path string, rec map[string]any, fields []cwl.RecordField) error {
	for _, rf := range fields {
		if len(rf.Symbols) == 0 {
			continue
		}
		raw, exists := rec[rf.Name]
		if !exists || raw == nil {
			continue
		}
		allowed := make(map[string]bool, len(rf.Symbols))
		for _, sym := range rf.Symbols {
			allowed[sym] = true
		}
		switch v := raw.(type) {
		case string:
			if !allowed[v] {
				return enumError(path+"."+rf.Name, "", v, rf.Symbols)
			}
		case []any:
			for i, elem := range v {
				sv, ok := elem.(string)
				if !ok {
					continue
				}
				if !allowed[sv] {
					return enumError(path+"."+rf.Name, fmt.Sprintf("[%d]", i), sv, rf.Symbols)
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
