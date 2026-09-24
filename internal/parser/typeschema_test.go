package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// jsonRoundTrip marshals then unmarshals v into a generic `any`, so that
// structural comparisons (reflect.DeepEqual) aren't tripped up by
// incidental Go-side type differences (e.g. []string vs []any) that don't
// matter once the value is actually JSON-serialized, per the #273 contract:
// "your normalizer MUST produce exactly `want` for every case (deep
// equality after JSON round trip)".
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func assertTypeSchema(t *testing.T, name string, got, want any) {
	t.Helper()
	gotJSON := jsonRoundTrip(t, got)
	wantJSON := jsonRoundTrip(t, want)
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Errorf("%s TypeSchema =\n  %#v\nwant\n  %#v", name, gotJSON, wantJSON)
	}
}

// --- Contract fixture: internal/validate/testdata/schemas.json "normalize" cases ---

func TestNormalizeType_Fixture(t *testing.T) {
	path := filepath.Join("..", "validate", "testdata", "schemas.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var fixture struct {
		Normalize []struct {
			Name       string           `json:"name"`
			Raw        any              `json:"raw"`
			SchemaDefs []map[string]any `json:"schemaDefs"`
			Want       any              `json:"want"`
		} `json:"normalize"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	if len(fixture.Normalize) == 0 {
		t.Fatal("no normalize cases found in schemas.json")
	}

	for _, tc := range fixture.Normalize {
		t.Run(tc.Name, func(t *testing.T) {
			defs := newSchemaDefs()
			for _, sd := range tc.SchemaDefs {
				defs.add(sd)
			}
			got := normalizeType(tc.Raw, defs, nil)
			assertTypeSchema(t, tc.Name, got, tc.Want)
		})
	}
}

// --- TypeSchema population at every input-construction site ---

func TestParser_WorkflowInputs_TypeSchema(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: Workflow
inputs:
  shorthand_string: string
  mapform_string:
    type: File
  optional_string:
    type: string?
  array_shorthand:
    type: File[]
  union_list:
    - int
    - string
outputs: {}
steps: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	wf := graph.Workflow

	cases := []struct {
		id   string
		want any
	}{
		{"shorthand_string", "string"},
		{"mapform_string", "File"},
		{"optional_string", []any{"null", "string"}},
		{"array_shorthand", map[string]any{"type": "array", "items": "File"}},
		{"union_list", []any{"int", "string"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			inp, ok := wf.Inputs[tc.id]
			if !ok {
				t.Fatalf("missing workflow input %q", tc.id)
			}
			assertTypeSchema(t, tc.id, inp.TypeSchema, tc.want)
		})
	}
}

func TestParser_ToolInputs_TypeSchema(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: CommandLineTool
id: tool
baseCommand: [echo]
inputs:
  shorthand_file: File
  optional_int:
    type: int?
outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool, ok := graph.Tools["tool"]
	if !ok {
		t.Fatal("tool 'tool' not found")
	}

	cases := []struct {
		id   string
		want any
	}{
		{"shorthand_file", "File"},
		{"optional_int", []any{"null", "int"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			inp, ok := tool.Inputs[tc.id]
			if !ok {
				t.Fatalf("missing tool input %q", tc.id)
			}
			assertTypeSchema(t, tc.id, inp.TypeSchema, tc.want)
		})
	}
}

func TestParser_ExpressionToolInputs_TypeSchema(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: ExpressionTool
id: tool
inputs:
  a: int
  b:
    type: int?
outputs:
  result:
    type: int
expression: "${return {'result': inputs.a};}"
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool, ok := graph.ExpressionTools["tool"]
	if !ok {
		t.Fatal("ExpressionTool 'tool' not found")
	}

	assertTypeSchema(t, "a", tool.Inputs["a"].TypeSchema, "int")
	assertTypeSchema(t, "b", tool.Inputs["b"].TypeSchema, []any{"null", "int"})
}

func TestParser_RecordFields_TypeSchema(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: CommandLineTool
id: tool
baseCommand: [echo]
inputs:
  rec:
    type:
      type: record
      fields:
        - name: a
          type: string?
        - name: b
          type: int
outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool := graph.Tools["tool"]
	inp, ok := tool.Inputs["rec"]
	if !ok {
		t.Fatal("missing input 'rec'")
	}

	wantSchema := map[string]any{
		"type": "record",
		"fields": []any{
			map[string]any{"name": "a", "type": []any{"null", "string"}},
			map[string]any{"name": "b", "type": "int"},
		},
	}
	assertTypeSchema(t, "rec", inp.TypeSchema, wantSchema)

	if len(inp.RecordFields) != 2 {
		t.Fatalf("RecordFields count = %d, want 2", len(inp.RecordFields))
	}
	for _, f := range inp.RecordFields {
		switch f.Name {
		case "a":
			assertTypeSchema(t, "rec.a", f.TypeSchema, []any{"null", "string"})
		case "b":
			assertTypeSchema(t, "rec.b", f.TypeSchema, "int")
		default:
			t.Errorf("unexpected field name %q", f.Name)
		}
	}
}

// --- SchemaDef collection: list-form, map-form, across $graph entries ---

func TestParser_SchemaDefInlining_ListForm(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: CommandLineTool
id: tool
baseCommand: [echo]
requirements:
  - class: SchemaDefRequirement
    types:
      - name: MyEnum
        type: enum
        symbols: [x, y]
inputs:
  choice: MyEnum
outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	inp := graph.Tools["tool"].Inputs["choice"]
	assertTypeSchema(t, "choice", inp.TypeSchema, map[string]any{
		"type":    "enum",
		"symbols": []any{"x", "y"},
	})
}

func TestParser_SchemaDefInlining_MapForm(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: CommandLineTool
id: tool
baseCommand: [echo]
requirements:
  SchemaDefRequirement:
    types:
      - name: MyEnum
        type: enum
        symbols: [x, y]
inputs:
  choice: MyEnum
outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	inp := graph.Tools["tool"].Inputs["choice"]
	assertTypeSchema(t, "choice", inp.TypeSchema, map[string]any{
		"type":    "enum",
		"symbols": []any{"x", "y"},
	})
}

func TestParser_SchemaDefInlining_AcrossGraphEntries(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
$graph:
  - id: main
    class: Workflow
    requirements:
      - class: SchemaDefRequirement
        types:
          - name: Shared
            type: enum
            symbols: [a, b]
    inputs: {}
    outputs: {}
    steps:
      run_tool:
        run: "#tool"
        in: {}
        out: []
  - id: tool
    class: CommandLineTool
    baseCommand: [echo]
    inputs:
      choice2: Shared
    outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool, ok := graph.Tools["tool"]
	if !ok {
		t.Fatal("tool 'tool' not found")
	}
	inp := tool.Inputs["choice2"]
	assertTypeSchema(t, "choice2", inp.TypeSchema, map[string]any{
		"type":    "enum",
		"symbols": []any{"a", "b"},
	})
}

// --- Conformance fixtures ---

func loadConformanceFixture(t *testing.T, rel string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "cwl-v1.2", "tests", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("conformance fixture %s not available: %v", path, err)
	}
	return data
}

func TestParser_ConformanceFixture_ImportSchemaDefPacked(t *testing.T) {
	p := testParser()
	data := loadConformanceFixture(t, "import_schema-def_packed.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}

	inp, ok := graph.Workflow.Inputs["capture_kit"]
	if !ok {
		t.Fatal("missing workflow input 'capture_kit'")
	}
	want := map[string]any{
		"type": "record",
		"fields": []any{
			map[string]any{"name": "#capture_kit.yml/capture_kit/bait", "type": "string"},
		},
	}
	assertTypeSchema(t, "capture_kit", inp.TypeSchema, want)
}

func TestParser_ConformanceFixture_TmapTool(t *testing.T) {
	p := testParser()
	data := loadConformanceFixture(t, "tmap-tool.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool, ok := graph.Tools["tool"]
	if !ok {
		t.Fatal("tool 'tool' not found (expected default id)")
	}
	inp, ok := tool.Inputs["stages"]
	if !ok {
		t.Fatal("missing input 'stages'")
	}

	arr, ok := inp.TypeSchema.(map[string]any)
	if !ok || arr["type"] != "array" {
		t.Fatalf("stages TypeSchema = %#v, want array", inp.TypeSchema)
	}
	stage, ok := arr["items"].(map[string]any)
	if !ok || stage["type"] != "record" {
		t.Fatalf("stages.items = %#v, want Stage record", arr["items"])
	}
	fields, ok := stage["fields"].([]any)
	if !ok || len(fields) != 3 {
		t.Fatalf("Stage.fields = %#v, want 3 fields", stage["fields"])
	}

	var algosField map[string]any
	for _, f := range fields {
		fm, _ := f.(map[string]any)
		if fm["name"] == "algos" {
			algosField = fm
		}
	}
	if algosField == nil {
		t.Fatal("Stage record missing 'algos' field")
	}
	algosArr, ok := algosField["type"].(map[string]any)
	if !ok || algosArr["type"] != "array" {
		t.Fatalf("algos field type = %#v, want array", algosField["type"])
	}
	algosItems, ok := algosArr["items"].([]any)
	if !ok || len(algosItems) != 4 {
		t.Fatalf("algos items = %#v, want union of 4 Map records", algosArr["items"])
	}
	for i, item := range algosItems {
		rec, ok := item.(map[string]any)
		if !ok || rec["type"] != "record" {
			t.Fatalf("algos item[%d] = %#v, want record", i, item)
		}
		recFields, ok := rec["fields"].([]any)
		if !ok {
			t.Fatalf("algos item[%d] fields = %#v", i, rec["fields"])
		}
		var algoField map[string]any
		for _, rf := range recFields {
			rfm, _ := rf.(map[string]any)
			if rfm["name"] == "algo" {
				algoField = rfm
			}
		}
		if algoField == nil {
			t.Fatalf("Map record[%d] missing 'algo' field", i)
		}
		algoEnum, ok := algoField["type"].(map[string]any)
		if !ok || algoEnum["type"] != "enum" {
			t.Fatalf("Map record[%d].algo type = %#v, want enum", i, algoField["type"])
		}
	}
}

func TestParser_ConformanceFixture_AnonEnumInsideArray(t *testing.T) {
	p := testParser()
	data := loadConformanceFixture(t, "anon_enum_inside_array.cwl")

	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool, ok := graph.Tools["tool"]
	if !ok {
		t.Fatal("tool 'tool' not found (expected default id)")
	}

	wantEnum := map[string]any{
		"type":    "enum",
		"symbols": []any{"homo_sapiens", "mus_musculus"},
	}

	first, ok := tool.Inputs["first"]
	if !ok {
		t.Fatal("missing input 'first'")
	}
	wantFirst := map[string]any{
		"type": "record",
		"fields": []any{
			map[string]any{"name": "species", "type": []any{wantEnum, "null"}},
		},
	}
	assertTypeSchema(t, "first", first.TypeSchema, wantFirst)

	second, ok := tool.Inputs["second"]
	if !ok {
		t.Fatal("missing input 'second'")
	}
	assertTypeSchema(t, "second", second.TypeSchema, []any{"null", wantEnum})
}

// --- Round trip: task.Tool JSON re-parse and ExpressionTool json.Unmarshal ---

func TestParseToolFromMap_TypeSchemaRoundTrip(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: CommandLineTool
id: tool
baseCommand: [echo]
requirements:
  - class: SchemaDefRequirement
    types:
      - name: MyEnum
        type: enum
        symbols: [x, y]
inputs:
  choice: MyEnum
  opt:
    type: string?
  rec:
    type:
      type: record
      fields:
        - name: a
          type: int
outputs: {}
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	tool := graph.Tools["tool"]

	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}

	reparsed, err := p.ParseToolFromMap(m)
	if err != nil {
		t.Fatalf("ParseToolFromMap: %v", err)
	}

	for id, orig := range tool.Inputs {
		got, ok := reparsed.Inputs[id]
		if !ok {
			t.Errorf("reparsed missing input %q", id)
			continue
		}
		assertTypeSchema(t, id, got.TypeSchema, orig.TypeSchema)
	}

	origRec := tool.Inputs["rec"]
	gotRec := reparsed.Inputs["rec"]
	if len(gotRec.RecordFields) != len(origRec.RecordFields) {
		t.Fatalf("RecordFields count = %d, want %d", len(gotRec.RecordFields), len(origRec.RecordFields))
	}
	for i := range origRec.RecordFields {
		assertTypeSchema(t, "rec.RecordFields", gotRec.RecordFields[i].TypeSchema, origRec.RecordFields[i].TypeSchema)
	}
}

func TestParseToolFromMap_NoTypeSchemaKey_StaysNil(t *testing.T) {
	p := testParser()
	m := map[string]any{
		"class":       "CommandLineTool",
		"cwlVersion":  "v1.2",
		"baseCommand": []any{"echo"},
		"inputs": map[string]any{
			"f": map[string]any{
				"type": "File?",
			},
		},
		"outputs": map[string]any{},
	}

	tool, err := p.ParseToolFromMap(m)
	if err != nil {
		t.Fatalf("ParseToolFromMap: %v", err)
	}
	inp, ok := tool.Inputs["f"]
	if !ok {
		t.Fatal("missing input 'f'")
	}
	if inp.Type != "File?" {
		t.Errorf("Type = %q, want File?", inp.Type)
	}
	if inp.TypeSchema != nil {
		t.Errorf("TypeSchema = %#v, want nil (no typeSchema key present)", inp.TypeSchema)
	}
}

func TestExpressionTool_JSONUnmarshal_KeepsTypeSchema(t *testing.T) {
	p := testParser()
	data := []byte(`cwlVersion: v1.2
class: ExpressionTool
id: tool
inputs:
  a:
    type: int?
outputs:
  result:
    type: int
expression: "${return {'result': inputs.a};}"
`)
	graph, err := p.ParseGraph(data)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	exprTool, ok := graph.ExpressionTools["tool"]
	if !ok {
		t.Fatal("ExpressionTool 'tool' not found")
	}

	raw, err := json.Marshal(exprTool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var reparsed cwl.ExpressionTool
	if err := json.Unmarshal(raw, &reparsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	assertTypeSchema(t, "a", reparsed.Inputs["a"].TypeSchema, exprTool.Inputs["a"].TypeSchema)
}
