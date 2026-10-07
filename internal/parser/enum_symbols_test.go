package parser

import (
	"io"
	"log/slog"
	"testing"
)

const enumSpec = `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: ["true"]
inputs:
  from_doc:
    type: string
    doc: "Type of ligand input [enum: smiles_list, ws_file, named_library] [bvbrc:enum]. More prose here."
  real_cwl_enum:
    type:
      type: enum
      symbols: [alpha, beta]
  array_of_enums:
    type:
      type: array
      items:
        type: enum
        symbols: [HA, NA]
  spaced_doc:
    type: string
    doc: "Recipe [enum:  auto ,  irma , reference_guided ]"
  no_enum:
    type: int?
    doc: "Return the top N results"
  empty_enum:
    type: string?
    doc: "Weird [enum: ] marker"
outputs: {}
`

// Every BV-BRC spec declares enums in doc strings rather than as CWL enum
// types -- 81 occurrences across 27 of 32 specs, zero real CWL enums -- so
// without the doc convention enum validation would apply to nothing.
func TestSymbolsFromSpec(t *testing.T) {
	g, err := New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(enumSpec))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	var tool = g.Tools["tool"]
	if tool == nil {
		for _, v := range g.Tools {
			tool = v
		}
	}

	cases := []struct {
		id   string
		want []string
	}{
		{"from_doc", []string{"smiles_list", "ws_file", "named_library"}},
		{"real_cwl_enum", []string{"alpha", "beta"}},
		{"array_of_enums", []string{"HA", "NA"}},
		{"spaced_doc", []string{"auto", "irma", "reference_guided"}},
		{"no_enum", nil},
		{"empty_enum", nil},
	}
	for _, tc := range cases {
		got := tool.Inputs[tc.id].Symbols
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.id, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", tc.id, got, tc.want)
				break
			}
		}
	}
}

func TestSymbolsFromDoc_Direct(t *testing.T) {
	cases := map[string][]string{
		"":                                 nil,
		"no marker here":                   nil,
		"[enum: a]":                        {"a"},
		"[enum: a, b]":                     {"a", "b"},
		"prose [enum: a,b] more prose":     {"a", "b"},
		"[enum:   a  ,  b  ]":              {"a", "b"},
		"[enum: ]":                         nil,
		"[enum: a, , b]":                   {"a", "b"},
		"[enum: 0, 1, 4, 11, 25]":          {"0", "1", "4", "11", "25"},
		"[enum: approved-drugs, small_db]": {"approved-drugs", "small_db"},
	}
	for doc, want := range cases {
		got := symbolsFromDoc(doc)
		if len(got) != len(want) {
			t.Errorf("%q: got %v, want %v", doc, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%q: got %v, want %v", doc, got, want)
				break
			}
		}
	}
}

// A bare CommandLineTool is wrapped in a synthetic Workflow, and the wrap
// dropped Symbols -- so GET /inputs reported no permitted values for any
// top-level enum on any of the 30 registered BV-BRC workflows, all of which
// are wrapped tools. Record fields were unaffected only because they travel
// inside RecordFields, which the wrap did copy.
func TestWrappedToolKeepsTopLevelSymbols(t *testing.T) {
	const spec = `
class: CommandLineTool
cwlVersion: v1.2
baseCommand: echo
inputs:
  aligner:
    type: string?
    doc: "Alignment program [enum: muscle, mafft] [bvbrc:enum]"
  libs:
    type:
      type: array
      items:
        type: record
        name: lib
        fields:
          - name: platform
            type: string?
            doc: "Platform [enum: illumina, nanopore] [bvbrc:enum]"
outputs: []
`
	doc, err := New(slog.New(slog.NewTextHandler(io.Discard, nil))).ParseGraph([]byte(spec))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.Workflow == nil {
		t.Fatal("tool was not wrapped into a workflow")
	}
	top, ok := doc.Workflow.Inputs["aligner"]
	if !ok {
		t.Fatal("aligner missing from the wrapped workflow inputs")
	}
	if len(top.Symbols) != 2 {
		t.Errorf("top-level symbols lost in the wrap: got %v, want [muscle mafft]", top.Symbols)
	}
	// And the record-field symbols that already worked must keep working.
	libs := doc.Workflow.Inputs["libs"]
	var pf []string
	for _, rf := range libs.RecordFields {
		if rf.Name == "platform" {
			pf = rf.Symbols
		}
	}
	if len(pf) != 2 {
		t.Errorf("record-field symbols: got %v, want [illumina nanopore]", pf)
	}
}
