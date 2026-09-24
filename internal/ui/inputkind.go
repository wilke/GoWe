package ui

import "strings"

// isFileType reports whether a workflow input's flattened CWL type string
// (model.WorkflowInput.Type, e.g. "File", "File?") denotes a File input.
// It also matches "File[]" — an existing quirk of the submission form: the
// input_field template checks isFileType before isArrayType, so a
// File-array input renders a single File picker rather than an array
// textarea. convertFormInput mirrors that same precedence so the value it
// builds matches the widget the form actually showed.
func isFileType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return t == "File" || strings.HasPrefix(t, "File[")
}

// isDirectoryType reports whether t denotes a (non-array) Directory input.
func isDirectoryType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return t == "Directory"
}

// isArrayType reports whether t denotes an array input (checked after
// isFileType/isDirectoryType by callers, so "File[]" is claimed by
// isFileType first — see its comment).
func isArrayType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return strings.HasSuffix(t, "[]") || strings.HasPrefix(t, "File[]")
}

// arrayItemType strips the optional "?" and a trailing "[]" from an array
// input's declared type, returning the item type used to convert each
// element parsed from an array-textarea value, e.g. "int[]" -> "int",
// "long[]?" -> "long". Types the UI doesn't specially convert (bare
// "string", or anything else) come back unchanged and are kept as raw
// strings by convertScalarInput.
func arrayItemType(t string) string {
	t = strings.TrimSuffix(t, "?")
	return strings.TrimSuffix(t, "[]")
}
