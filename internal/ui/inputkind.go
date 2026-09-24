package ui

import "strings"

// isFileType reports whether a workflow input's flattened CWL type string
// (model.WorkflowInput.Type, e.g. "File", "File?") denotes a single
// (non-array) File input. "File[]"/"File[]?" are array types — see
// isArrayType — and are handled by convertArrayInput/the array textarea
// widget, not this one (#273: File[]/Directory[] must build a JSON array of
// File/Directory objects, not a single one).
func isFileType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return t == "File"
}

// isDirectoryType reports whether t denotes a (non-array) Directory input.
func isDirectoryType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return t == "Directory"
}

// isArrayType reports whether t denotes an array input, e.g. "int[]",
// "File[]", "File[]?".
func isArrayType(t string) bool {
	t = strings.TrimSuffix(t, "?")
	return strings.HasSuffix(t, "[]")
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
