package parser

import (
	"io"
	"log/slog"
	"testing"
)

// TestRecordFieldsFromArrayOfRecords is a regression guard.
//
// An array-of-records input parsed with RecordFields empty, because the parser
// only looked for fields on a bare record type. Every consumer short-circuits
// on an empty slice, so ApplyRecordFieldDefaults, ValidateRecordFields and
// ValidateRecordShape were all silently inert for 31 of the 32 registered
// BV-BRC specs. Malformed payloads reached BV-BRC and died in Perl preflight.
const geneTreeShape = `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: ["true"]
inputs:
  sequences:
    type:
      type: array
      items:
        type: record
        name: sequence_input
        fields:
          - name: filename
            type: string
          - name: type
            type: string
  single_rec:
    type:
      type: record
      name: solo
      fields:
        - name: path
          type: string
  plain:
    type: string
outputs: {}
`

func TestRecordFieldsFromArrayOfRecords(t *testing.T) {
	g, err := New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(geneTreeShape))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	if len(g.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(g.Tools))
	}
	var tool = g.Tools["tool"]
	if tool == nil {
		for _, v := range g.Tools {
			tool = v
		}
	}

	t.Run("array of records", func(t *testing.T) {
		ip, ok := tool.Inputs["sequences"]
		if !ok {
			t.Fatal("sequences input missing")
		}
		if len(ip.RecordFields) != 2 {
			t.Fatalf("RecordFields = %d, want 2 (filename, type) — an empty "+
				"slice disables all record validation", len(ip.RecordFields))
		}
		names := map[string]bool{}
		for _, f := range ip.RecordFields {
			names[f.Name] = true
		}
		for _, want := range []string{"filename", "type"} {
			if !names[want] {
				t.Errorf("missing record field %q", want)
			}
		}
	})

	t.Run("bare record still works", func(t *testing.T) {
		ip := tool.Inputs["single_rec"]
		if len(ip.RecordFields) != 1 || ip.RecordFields[0].Name != "path" {
			t.Errorf("RecordFields = %+v, want one field named path", ip.RecordFields)
		}
	})

	t.Run("non-record input stays empty", func(t *testing.T) {
		if ip := tool.Inputs["plain"]; len(ip.RecordFields) != 0 {
			t.Errorf("RecordFields = %+v, want empty for a string input", ip.RecordFields)
		}
	})
}

func TestRecordFieldsFromType_Direct(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want int
	}{
		{"nil", nil, 0},
		{"bare record", map[string]any{
			"type":   "record",
			"fields": []any{map[string]any{"name": "a", "type": "string"}},
		}, 1},
		{"array of records", map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "record",
				"fields": []any{map[string]any{"name": "a", "type": "string"},
					map[string]any{"name": "b", "type": "int"}},
			},
		}, 2},
		{"array of scalars", map[string]any{"type": "array", "items": "string"}, 0},
		{"plain string type", map[string]any{"type": "string"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(recordFieldsFromType(tc.in)); got != tc.want {
				t.Errorf("got %d fields, want %d", got, tc.want)
			}
		})
	}
}
