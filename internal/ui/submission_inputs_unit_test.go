package ui

import (
	"reflect"
	"testing"
)

// TestConvertFormInput exercises convertFormInput directly (no store round
// trip) so the exact in-memory Go type each conversion produces is
// checkable — in particular that "long" converts to int64 like "int"
// converts to int, and that a value that fails to parse as its declared
// numeric type is kept as the raw string rather than dropped, so
// validation can report it (#273).
func TestConvertFormInput(t *testing.T) {
	tests := []struct {
		name    string
		val     string
		cwlType string
		want    any
	}{
		{"file workspace-form path", "/awilke@bvbrc/home/x.txt", "File",
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/x.txt"}},
		{"file optional type", "/awilke@bvbrc/home/x.txt", "File?",
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/x.txt"}},
		{"file already a ws URI is kept", "ws:///awilke@bvbrc/home/x.txt", "File",
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/x.txt"}},
		{"file local path unchanged", "/data/local.txt", "File",
			map[string]any{"class": "File", "location": "/data/local.txt"}},
		{"directory workspace-form path", "/awilke@bvbrc/home/out", "Directory",
			map[string]any{"class": "Directory", "location": "ws:///awilke@bvbrc/home/out"}},
		{"int", "42", "int", 42},
		{"int optional type", "42", "int?", 42},
		{"int bad parse keeps raw string", "abc", "int", "abc"},
		{"long converts like int but to int64", "9999999999", "long", int64(9999999999)},
		{"long bad parse keeps raw string", "abc", "long", "abc"},
		{"float", "3.5", "float", 3.5},
		{"double", "2.25", "double", 2.25},
		{"double bad parse keeps raw string", "not-a-number", "double", "not-a-number"},
		{"boolean true", "true", "boolean", true},
		{"boolean on", "on", "boolean", true},
		{"boolean 1", "1", "boolean", true},
		{"boolean anything else is false", "nope", "boolean", false},
		{"string passthrough", "hello", "string", "hello"},
		{"array of int, JSON form", "[1,2,3]", "int[]", []any{float64(1), float64(2), float64(3)}},
		{"array of int, line form", "1\n2\n3", "int[]", []any{1, 2, 3}},
		{"file array, line form", "/awilke@bvbrc/home/a.txt\n/awilke@bvbrc/home/b.txt", "File[]", []any{
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/a.txt"},
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/b.txt"},
		}},
		{"file array, optional type", "/awilke@bvbrc/home/a.txt", "File[]?", []any{
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/a.txt"},
		}},
		{"directory array, line form", "/awilke@bvbrc/home/a\n/awilke@bvbrc/home/b", "Directory[]", []any{
			map[string]any{"class": "Directory", "location": "ws:///awilke@bvbrc/home/a"},
			map[string]any{"class": "Directory", "location": "ws:///awilke@bvbrc/home/b"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertFormInput(tt.val, tt.cwlType)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("convertFormInput(%q, %q) = %#v (%T), want %#v (%T)",
					tt.val, tt.cwlType, got, got, tt.want, tt.want)
			}
		})
	}
}

// TestConvertArrayInput covers the array-textarea parsing rule directly:
// JSON array when the trimmed value starts with '[', otherwise one value
// per non-empty line, with per-item conversion by itemType.
func TestConvertArrayInput(t *testing.T) {
	tests := []struct {
		name     string
		val      string
		itemType string
		want     []any
	}{
		{"json array of bare numbers", "[1,2,3]", "int", []any{float64(1), float64(2), float64(3)}},
		{"json array of quoted numbers converted by item type", `["1","2"]`, "long", []any{int64(1), int64(2)}},
		{"json array leading/trailing whitespace", "  [1,2]  ", "int", []any{float64(1), float64(2)}},
		{"line form", "1\n2\n3", "int", []any{1, 2, 3}},
		{"line form skips blank lines", "a\n\nb\n", "string", []any{"a", "b"}},
		{"malformed json falls back to line parsing", "[not valid json", "string", []any{"[not valid json"}},
		{"line form of File items", "/awilke@bvbrc/home/a.txt\n/awilke@bvbrc/home/b.txt", "File", []any{
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/a.txt"},
			map[string]any{"class": "File", "location": "ws:///awilke@bvbrc/home/b.txt"},
		}},
		{"line form of Directory items", "/awilke@bvbrc/home/a\n/awilke@bvbrc/home/b", "Directory", []any{
			map[string]any{"class": "Directory", "location": "ws:///awilke@bvbrc/home/a"},
			map[string]any{"class": "Directory", "location": "ws:///awilke@bvbrc/home/b"},
		}},
		{"line form of boolean items", "true\nfalse", "boolean", []any{true, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertArrayInput(tt.val, tt.itemType)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("convertArrayInput(%q, %q) = %#v, want %#v", tt.val, tt.itemType, got, tt.want)
			}
		})
	}
}

func TestArrayItemType(t *testing.T) {
	tests := []struct{ in, want string }{
		{"int[]", "int"},
		{"int[]?", "int"},
		{"long[]", "long"},
		{"File[]", "File"},
		{"string[]", "string"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := arrayItemType(tt.in); got != tt.want {
				t.Errorf("arrayItemType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInputKindClassification(t *testing.T) {
	tests := []struct {
		t                      string
		file, directory, array bool
	}{
		{"File", true, false, false},
		{"File?", true, false, false},
		{"File[]", false, false, true}, // array of File — see inputkind.go and #273
		{"File[]?", false, false, true},
		{"Directory", false, true, false},
		{"Directory?", false, true, false},
		{"Directory[]", false, false, true},
		{"int[]", false, false, true},
		{"int[]?", false, false, true},
		{"string", false, false, false},
		{"long", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.t, func(t *testing.T) {
			if got := isFileType(tt.t); got != tt.file {
				t.Errorf("isFileType(%q) = %v, want %v", tt.t, got, tt.file)
			}
			if got := isDirectoryType(tt.t); got != tt.directory {
				t.Errorf("isDirectoryType(%q) = %v, want %v", tt.t, got, tt.directory)
			}
			if got := isArrayType(tt.t); got != tt.array {
				t.Errorf("isArrayType(%q) = %v, want %v", tt.t, got, tt.array)
			}
		})
	}
}
