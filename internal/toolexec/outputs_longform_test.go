package toolexec

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/toolmerge"
	"github.com/me/gowe/pkg/cwl"
)

const longFormHeader = `cwlVersion: v1.2
class: CommandLineTool
baseCommand: "true"
inputs: []
outputs:
`

func parseTool(t *testing.T, outputsYAML string) *cwl.CommandLineTool {
	t.Helper()
	p := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	g, err := p.ParseGraph([]byte(longFormHeader + outputsYAML))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	for _, tool := range g.Tools {
		return tool
	}
	t.Fatal("no tool parsed")
	return nil
}

// TestCollectOutputs_LongFormTypes parses real YAML (shorthand and long-form
// type spellings) and checks glob collection + validation for 0, 1 and 2
// matches. Long-form types leave ToolOutputParam.Type empty.
func TestCollectOutputs_LongFormTypes(t *testing.T) {
	spellings := map[string]string{
		"optional list union": `  o:
    type: ["null", File]
    outputBinding: {glob: "*.txt"}
`,
		"optional map union": `  o:
    type:
      - "null"
      - File
    outputBinding: {glob: "*.txt"}
`,
		"long array": `  o:
    type: {type: array, items: File}
    outputBinding: {glob: "*.txt"}
`,
		"shorthand array": `  o:
    type: File[]
    outputBinding: {glob: "*.txt"}
`,
		"optional array shorthand": `  o:
    type: File[]?
    outputBinding: {glob: "*.txt"}
`,
		"optional array long": `  o:
    type:
      - "null"
      - {type: array, items: File}
    outputBinding: {glob: "*.txt"}
`,
	}
	type want struct {
		failsAt map[int]bool // match count -> expect error
		kind    map[int]string
	}
	// kind: "nil", "file", "array<N>" (checked loosely below)
	optionalFile := want{kind: map[int]string{0: "nil", 1: "file"}, failsAt: map[int]bool{2: true}}
	array := want{kind: map[int]string{0: "array0", 1: "array1", 2: "array2"}}
	optArrayShort := want{kind: map[int]string{0: "nil", 1: "array1", 2: "array2"}}
	wants := map[string]want{
		"optional list union":      optionalFile,
		"optional map union":       optionalFile,
		"long array":               array,
		"shorthand array":          array,
		"optional array shorthand": optArrayShort,
		"optional array long":      optArrayShort,
	}

	for name, yaml := range spellings {
		for n := 0; n <= 2; n++ {
			t.Run(name+"/"+string(rune('0'+n))+" matches", func(t *testing.T) {
				tool := parseTool(t, yaml)
				workDir := t.TempDir()
				for i := 0; i < n; i++ {
					if err := os.WriteFile(filepath.Join(workDir, string(rune('a'+i))+".txt"), []byte("x"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				w := wants[name]
				outs, err := NewExecutor(slog.Default()).CollectOutputs(tool, workDir, nil, 0, workDir, nil)
				if w.failsAt[n] {
					if err == nil {
						t.Fatalf("want error, got %v", outs)
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				got := outs["o"]
				switch k := w.kind[n]; {
				case k == "nil":
					if got != nil {
						t.Errorf("want nil, got %v", got)
					}
				case k == "file":
					if m, ok := got.(map[string]any); !ok || m["class"] != "File" {
						t.Errorf("want File, got %T %v", got, got)
					}
				case strings.HasPrefix(k, "array"):
					arr, ok := got.([]map[string]any)
					if !ok || len(arr) != int(k[len(k)-1]-'0') {
						t.Errorf("want %s, got %T %v", k, got, got)
					}
				}
			})
		}
	}
}

// TestParseToolRoundTrip locks in the worker contract: the output TypeSchema
// survives ParseGraph -> StructToToolMap -> ParseToolFromMap, and validation
// still fires on the re-parsed tool.
func TestParseToolRoundTrip(t *testing.T) {
	tool := parseTool(t, `  must:
    type: File
    outputBinding: {glob: "none.txt"}
  opt:
    type: ["null", File]
    outputBinding: {glob: "none.txt"}
`)
	m, err := toolmerge.StructToToolMap(tool)
	if err != nil {
		t.Fatal(err)
	}
	p := parser.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	re, err := p.ParseToolFromMap(m)
	if err != nil {
		t.Fatalf("ParseToolFromMap: %v", err)
	}
	for id, o := range tool.Outputs {
		if o.TypeSchema == nil || re.Outputs[id].TypeSchema == nil {
			t.Fatalf("output %s lost TypeSchema (orig=%v reparsed=%v)", id, o.TypeSchema, re.Outputs[id].TypeSchema)
		}
	}
	_, err = NewExecutor(slog.Default()).CollectOutputs(re, t.TempDir(), nil, 0, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), `output "must"`) {
		t.Fatalf("want failure naming must, got %v", err)
	}
}
