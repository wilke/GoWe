package ui

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/me/gowe/internal/wslocation"
	"github.com/me/gowe/pkg/model"
)

// convertFormInput builds a typed CWL value from a single submission-form
// field (r.FormValue("inputs["+inp.ID+"]")), based on the workflow input's
// declared, flattened CWL type string (model.WorkflowInput.Type, e.g.
// "File", "File?", "int[]", "long"). It mirrors the widget the
// "components/input_field" template actually rendered for that type (see
// isFileType/isDirectoryType/isArrayType in inputkind.go):
//
//   - File/Directory text box -> {"class": "File"|"Directory", "location":
//     wslocation.Resolve(val)}, so a typed workspace-form path becomes a
//     ws:// location exactly like the CLI's --workspace-upload path (#273).
//   - array textarea (including "File[]"/"File[]?"/"Directory[]") -> a JSON
//     array when the trimmed value starts with '[', otherwise one value per
//     non-empty line; each item is converted per the array's item type, so
//     a File[]/Directory[] input builds one {class, location} object per
//     non-empty item (#273).
//   - everything else -> convertScalarInput.
func convertFormInput(val, cwlType string) any {
	switch {
	case isFileType(cwlType):
		return map[string]any{"class": "File", "location": wslocation.Resolve(val)}
	case isDirectoryType(cwlType):
		return map[string]any{"class": "Directory", "location": wslocation.Resolve(val)}
	case isArrayType(cwlType):
		return convertArrayInput(val, arrayItemType(cwlType))
	default:
		return convertScalarInput(val, cwlType)
	}
}

// convertArrayInput parses an array-textarea value. A trimmed value
// starting with '[' is parsed as a JSON array; on success, each string
// element is converted per itemType (non-string elements, e.g. bare JSON
// numbers/booleans, are left as the JSON decoder produced them). Any other
// value — including one that starts with '[' but fails to parse as JSON —
// is split into one item per non-empty line, each converted per itemType,
// so a malformed value is preserved for validation to report rather than
// silently dropped or garbled.
func convertArrayInput(val, itemType string) []any {
	trimmed := strings.TrimSpace(val)
	if strings.HasPrefix(trimmed, "[") {
		var arr []any
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			for i, item := range arr {
				if s, ok := item.(string); ok {
					arr[i] = convertArrayItem(s, itemType)
				}
			}
			return arr
		}
	}
	lines := strings.Split(val, "\n")
	result := make([]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result = append(result, convertArrayItem(line, itemType))
	}
	return result
}

// convertArrayItem converts a single array element (already a string, from
// either a line or a JSON string element) per the array's item type.
func convertArrayItem(item, itemType string) any {
	switch {
	case isFileType(itemType):
		return map[string]any{"class": "File", "location": wslocation.Resolve(item)}
	case isDirectoryType(itemType):
		return map[string]any{"class": "Directory", "location": wslocation.Resolve(item)}
	default:
		return convertScalarInput(item, itemType)
	}
}

// convertScalarInput converts a single form value per its declared scalar
// CWL type (int, long, float, double, boolean; anything else, including
// plain "string", is kept as-is). A value that fails to parse as its
// declared numeric type is returned unchanged as a string rather than
// dropped, so input validation reports the mismatch instead of the value
// silently vanishing.
func convertScalarInput(val, cwlType string) any {
	switch strings.TrimSuffix(cwlType, "?") {
	case "int":
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
		return val
	case "long":
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			return n
		}
		return val
	case "float", "double":
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		return val
	case "boolean":
		return val == "true" || val == "on" || val == "1"
	default:
		return val
	}
}

// formatFieldErrors renders validate.SubmissionInputs field errors for
// display in the submission form/notice: "input <id><path>: <message>" per
// error (Path already carries its own "." or "[N]" separator when set),
// joined with "; ". The result lands inside a {{ }} action in an
// html/template page, which auto-escapes it, and is also safe to
// url.QueryEscape for a "?error="/"?warning=" flash redirect.
func formatFieldErrors(errs []model.FieldError) string {
	parts := make([]string, 0, len(errs))
	for _, fe := range errs {
		parts = append(parts, "input "+fe.Field+fe.Path+": "+fe.Message)
	}
	return strings.Join(parts, "; ")
}
