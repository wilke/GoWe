package validate

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
)

// Proves the fix against the REAL registered Gene Tree spec, not a fixture:
// the enum lives in a doc string on a record field, and the value below is the
// one an agent actually produced for an aligned FASTA.
func TestRealGeneTreeCWL_RecordFieldEnum(t *testing.T) {
	raw, err := os.ReadFile("../../cwl/bvbrc-registered/tmp_workflow_gene_tree.cwl")
	if err != nil {
		t.Skipf("spec not available: %v", err)
	}
	doc, err := parser.New(slog.New(slog.NewTextHandler(os.Stderr, nil))).ParseGraph(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var tool *cwl.CommandLineTool
	for _, clt := range doc.Tools {
		tool = clt
		break
	}
	if tool == nil {
		t.Fatal("no tool in the graph")
	}

	seq, ok := tool.Inputs["sequences"]
	if !ok {
		t.Fatal("no sequences input")
	}
	var typeField *cwl.RecordField
	for i := range seq.RecordFields {
		if seq.RecordFields[i].Name == "type" {
			typeField = &seq.RecordFields[i]
		}
	}
	if typeField == nil {
		t.Fatal("sequences has no `type` field")
	}
	if len(typeField.Symbols) == 0 {
		t.Fatal("sequences[].type extracted NO symbols from its doc string")
	}
	t.Logf("sequences[].type symbols: %v", typeField.Symbols)

	bad := map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/x/aligned.afa", "type": "TOTALLY_BOGUS_TYPE"},
		},
	}
	err = ValidateEnumValues(tool, bad)
	if err == nil {
		t.Fatal("an undeclared sequences[].type was ACCEPTED -- the fix is not live")
	}
	t.Logf("rejected as expected: %v", err)
	if !strings.Contains(err.Error(), "sequences[0].type") {
		t.Errorf("message should name the path, got %q", err)
	}

	// And the real-world wrong-but-declared value must still pass: it is a
	// legal symbol. Catching THAT needs semantics, not schema validation.
	ok2 := map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/x/aligned.afa", "type": "feature_dna_fasta"},
		},
	}
	if err := ValidateEnumValues(tool, ok2); err != nil {
		t.Errorf("a declared value was rejected: %v", err)
	}
}
