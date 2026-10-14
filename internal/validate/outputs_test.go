package validate

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func toolWith(outs map[string]any) *cwl.CommandLineTool {
	t := &cwl.CommandLineTool{ID: "t", Outputs: map[string]cwl.ToolOutputParam{}}
	for id, schema := range outs {
		t.Outputs[id] = cwl.ToolOutputParam{TypeSchema: schema}
	}
	return t
}

func arr(items any) map[string]any { return map[string]any{"type": "array", "items": items} }

func TestToolOutputs(t *testing.T) {
	enum := map[string]any{"type": "enum", "symbols": []any{"red", "blue"}}
	rec := map[string]any{"type": "record", "fields": []any{
		map[string]any{"name": "n", "type": "int"},
		map[string]any{"name": "f", "type": []any{"null", "File"}},
	}}
	file := map[string]any{"class": "File", "location": "file:///x"}

	tests := []struct {
		name    string
		schemas map[string]any
		outputs map[string]any
		wantErr string // substring; "" = success
		wantKey []string
		dropped []string
	}{
		{"missing required File glob (#279)", map[string]any{"d": "Directory", "must": "File"},
			map[string]any{"d": map[string]any{"class": "Directory", "location": "file:///d"}, "must": nil},
			`output "must"`, nil, nil},
		{"required File absent entirely", map[string]any{"must": "File"},
			map[string]any{}, `output "must"`, nil, nil},
		{"missing required in cwl.output.json (#310)", map[string]any{"req": "string", "col": enum},
			map[string]any{"col": "red", "extra": 1.0}, `output "req"`, nil, nil},
		{"bad enum (#310)", map[string]any{"req": "string", "col": enum},
			map[string]any{"req": "x", "col": "green"}, `output "col"`, nil, nil},
		{"valid enum", map[string]any{"col": enum},
			map[string]any{"col": "blue"}, "", []string{"col"}, nil},
		{"wrong primitive", map[string]any{"s": "string"},
			map[string]any{"s": 3.0}, `output "s"`, nil, nil},
		{"wrong boolean", map[string]any{"b": "boolean"},
			map[string]any{"b": "true"}, `output "b"`, nil, nil},
		{"int where float expected passes", map[string]any{"f": "float", "d": "double"},
			map[string]any{"f": int64(3), "d": 2.0}, "", []string{"f", "d"}, nil},
		{"integral number for int passes", map[string]any{"i": "int", "l": "long"},
			map[string]any{"i": 4.0, "l": 5.0}, "", []string{"i", "l"}, nil},
		{"fractional number for int fails", map[string]any{"i": "int"},
			map[string]any{"i": 4.5}, `output "i"`, nil, nil},
		{"wrong File class", map[string]any{"f": "File"},
			map[string]any{"f": map[string]any{"class": "Directory", "location": "x"}}, `output "f"`, nil, nil},
		{"File ok", map[string]any{"f": "File"},
			map[string]any{"f": file}, "", []string{"f"}, nil},
		{"stdout pseudo type", map[string]any{"o": "stdout"},
			map[string]any{"o": file}, "", []string{"o"}, nil},
		{"nested array ok (typed Go slices)", map[string]any{"a": arr(arr("int"))},
			map[string]any{"a": [][]int{{1, 2}, {3}}}, "", []string{"a"}, nil},
		{"nested array bad item", map[string]any{"a": arr(arr("int"))},
			map[string]any{"a": []any{[]any{1.0, "x"}}}, `output "a"[0][1]`, nil, nil},
		{"array of File from []map", map[string]any{"a": arr("File")},
			map[string]any{"a": []map[string]any{file}}, "", []string{"a"}, nil},
		{"empty array for required array ok", map[string]any{"a": arr("File")},
			map[string]any{"a": []any{}}, "", []string{"a"}, nil},
		{"record ok", map[string]any{"r": rec},
			map[string]any{"r": map[string]any{"n": 1.0}}, "", []string{"r"}, nil},
		{"record missing required field", map[string]any{"r": rec},
			map[string]any{"r": map[string]any{"f": file}}, `output "r".n`, nil, nil},
		{"record wrong field type", map[string]any{"r": rec},
			map[string]any{"r": map[string]any{"n": "x"}}, `output "r".n`, nil, nil},
		{"optional null ok", map[string]any{"o": []any{"null", "File"}},
			map[string]any{"o": nil}, "", []string{"o"}, nil},
		{"nil typed slice serialises as null: required array fails", map[string]any{"a": arr("File")},
			map[string]any{"a": []map[string]any(nil)}, `output "a"`, nil, nil},
		{"optional absent ok", map[string]any{"o": []any{"null", "File"}},
			map[string]any{}, "", []string{}, nil},
		{"optional wrong type", map[string]any{"o": []any{"null", "File"}},
			map[string]any{"o": 3.0}, `output "o"`, nil, nil},
		{"union either member", map[string]any{"u": []any{"string", "int"}},
			map[string]any{"u": 3.0}, "", []string{"u"}, nil},
		{"Any accepts anything", map[string]any{"a": "Any"},
			map[string]any{"a": map[string]any{"x": []any{1.0}}}, "", []string{"a"}, nil},
		{"Any required null fails", map[string]any{"a": "Any"},
			map[string]any{"a": nil}, `output "a"`, nil, nil},
		{"unresolved named type lenient", map[string]any{"n": "SomeSchema"},
			map[string]any{"n": "whatever"}, "", []string{"n"}, nil},
		{"nil schema not validated", map[string]any{"n": nil},
			map[string]any{}, "", []string{}, nil},
		{"extra key dropped", map[string]any{"a": "string"},
			map[string]any{"a": "x", "extra": 1.0, "other": "y"}, "", []string{"a"}, []string{"extra", "other"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, nil))
			got, err := ToolOutputs(toolWith(tc.schemas), tc.outputs, logger)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (outputs %v)", tc.wantErr, got)
				}
				if !errors.Is(err, ErrOutputValidation) {
					t.Errorf("error does not wrap ErrOutputValidation: %v", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.wantKey) {
				t.Errorf("got keys %v, want %v", got, tc.wantKey)
			}
			for _, k := range tc.wantKey {
				if _, ok := got[k]; !ok {
					t.Errorf("missing key %q in %v", k, got)
				}
			}
			for _, k := range tc.dropped {
				if _, ok := got[k]; ok {
					t.Errorf("undeclared key %q not dropped", k)
				}
				if n := strings.Count(buf.String(), "output="+k); n != 1 {
					t.Errorf("want exactly 1 WARN for %q, got %d: %s", k, n, buf.String())
				}
			}
			if len(tc.dropped) == 0 && strings.Contains(buf.String(), "WARN") {
				t.Errorf("unexpected WARN: %s", buf.String())
			}
		})
	}
}

func TestToolOutputs_NoValueInError(t *testing.T) {
	_, err := ToolOutputs(toolWith(map[string]any{"s": "int"}), map[string]any{"s": "SECRET-VALUE"}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-VALUE") {
		t.Fatalf("want error without the value, got %v", err)
	}
}
