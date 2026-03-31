package parser

import (
	"log/slog"
	"testing"
)

func TestNullableArrayItemBinding(t *testing.T) {
	cwlData := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: [test]
inputs:
  protein:
    type:
      - "null"
      - type: array
        items: File
        inputBinding:
          prefix: --protein
          position: 2
    doc: "test input"
outputs: {}
`
	p := New(slog.Default())
	graph, err := p.ParseGraphWithBase([]byte(cwlData), "/tmp")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	for id, tool := range graph.Tools {
		t.Logf("Tool: %s", id)
		inp, ok := tool.Inputs["protein"]
		if !ok {
			t.Fatal("protein input not found")
		}
		t.Logf("Type: %s", inp.Type)
		t.Logf("ItemInputBinding: %+v", inp.ItemInputBinding)
		if inp.ItemInputBinding == nil {
			t.Fatal("ItemInputBinding is nil — nullable array binding not extracted")
		}
		if inp.ItemInputBinding.Prefix != "--protein" {
			t.Fatalf("expected prefix --protein, got %s", inp.ItemInputBinding.Prefix)
		}
		t.Log("OK: ItemInputBinding correctly parsed for nullable array")
	}
}

func TestNullableArrayItemBindingFromJSON(t *testing.T) {
	// Simulate the re-parse path: tool stored as JSON with type as string
	// and itemInputBinding as a separate key.
	toolMap := map[string]any{
		"class":       "CommandLineTool",
		"cwlVersion":  "v1.2",
		"baseCommand": []any{"test"},
		"inputs": map[string]any{
			"protein": map[string]any{
				"type":           "File[]?",
				"doc":            "test",
				"arrayItemTypes": []any{"File"},
				"itemInputBinding": map[string]any{
					"prefix":   "--protein",
					"position": float64(2),
				},
			},
		},
		"outputs": map[string]any{},
	}
	p := New(slog.Default())
	tool, err := p.ParseToolFromMap(toolMap)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	inp, ok := tool.Inputs["protein"]
	if !ok {
		t.Fatal("protein input not found")
	}
	t.Logf("Type: %s", inp.Type)
	t.Logf("ItemInputBinding: %+v", inp.ItemInputBinding)
	t.Logf("InputBinding: %+v", inp.InputBinding)
	if inp.ItemInputBinding == nil {
		t.Fatal("ItemInputBinding is nil — JSON recovery path failed")
	}
	if inp.ItemInputBinding.Prefix != "--protein" {
		t.Fatalf("expected prefix --protein, got %s", inp.ItemInputBinding.Prefix)
	}
	t.Log("OK: ItemInputBinding correctly recovered from JSON")
}
