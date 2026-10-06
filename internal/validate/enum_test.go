package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func enumTool(symbols ...string) *cwl.CommandLineTool {
	return &cwl.CommandLineTool{Inputs: map[string]cwl.ToolInputParam{
		"ligand_library_type": {Type: "string", Symbols: symbols},
	}}
}

// TestValidateEnumValues_RejectsUndeclaredValue is the regression guard.
//
// Before this, an enum violation was invisible to GoWe: the submission was
// accepted, reached BV-BRC, and failed inside the app minutes later. Docking
// job 23710910 died with "Unknown ligand library type selected named_library".
func TestValidateEnumValues_RejectsUndeclaredValue(t *testing.T) {
	tool := enumTool("smiles_list", "ws_file")
	err := ValidateEnumValues(tool, map[string]any{"ligand_library_type": "named_library"})
	if err == nil {
		t.Fatal("undeclared enum value accepted")
	}
	if !errors.Is(err, ErrInputValidation) {
		t.Errorf("does not wrap ErrInputValidation: %v", err)
	}
	// The message must name the offender and list the alternatives, so an LLM
	// can self-correct without re-reading the schema.
	for _, want := range []string{"ligand_library_type", "named_library", "smiles_list", "ws_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

func TestValidateEnumValues_AcceptsDeclaredValues(t *testing.T) {
	tool := enumTool("smiles_list", "ws_file", "named_library")
	for _, v := range []string{"smiles_list", "ws_file", "named_library"} {
		if err := ValidateEnumValues(tool, map[string]any{"ligand_library_type": v}); err != nil {
			t.Errorf("%s rejected: %v", v, err)
		}
	}
}

func TestValidateEnumValues_ArrayOfEnums(t *testing.T) {
	tool := &cwl.CommandLineTool{Inputs: map[string]cwl.ToolInputParam{
		"segments": {Type: "string[]?", Symbols: []string{"HA", "NA", "PB1"}},
	}}
	if err := ValidateEnumValues(tool, map[string]any{"segments": []any{"HA", "NA"}}); err != nil {
		t.Errorf("valid array rejected: %v", err)
	}
	err := ValidateEnumValues(tool, map[string]any{"segments": []any{"HA", "XX"}})
	if err == nil {
		t.Fatal("array containing an undeclared value accepted")
	}
	if !strings.Contains(err.Error(), "segments[1]") {
		t.Errorf("error should point at the offending index: %v", err)
	}
}

func TestValidateEnumValues_SkipsWhatItShould(t *testing.T) {
	tool := enumTool("smiles_list", "ws_file")

	t.Run("absent input", func(t *testing.T) {
		if err := ValidateEnumValues(tool, map[string]any{}); err != nil {
			t.Errorf("absent input should be ignored: %v", err)
		}
	})
	t.Run("nil value", func(t *testing.T) {
		if err := ValidateEnumValues(tool, map[string]any{"ligand_library_type": nil}); err != nil {
			t.Errorf("nil should be ignored: %v", err)
		}
	})
	t.Run("input with no declared symbols is inert", func(t *testing.T) {
		plain := &cwl.CommandLineTool{Inputs: map[string]cwl.ToolInputParam{
			"top_n": {Type: "int?"},
		}}
		if err := ValidateEnumValues(plain, map[string]any{"top_n": "anything"}); err != nil {
			t.Errorf("no symbols means no checking: %v", err)
		}
	})
	t.Run("non-string value is left to type checking", func(t *testing.T) {
		if err := ValidateEnumValues(tool, map[string]any{"ligand_library_type": 42}); err != nil {
			t.Errorf("non-string should be left alone: %v", err)
		}
	})
	t.Run("non-string array element is skipped", func(t *testing.T) {
		tl := &cwl.CommandLineTool{Inputs: map[string]cwl.ToolInputParam{
			"segments": {Type: "string[]?", Symbols: []string{"HA"}},
		}}
		if err := ValidateEnumValues(tl, map[string]any{"segments": []any{"HA", 7}}); err != nil {
			t.Errorf("non-string element should be skipped: %v", err)
		}
	})
}
