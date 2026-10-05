package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

// toolWithRecordInput builds a tool whose single input is a record (or record
// array), modelled on Gene Tree's `sequences`.
func toolWithRecordInput(typeStr string) *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			"sequences": {
				Type: typeStr,
				RecordFields: []cwl.RecordField{
					{Name: "filename", Type: "string"},
					{Name: "type", Type: "string"},
				},
			},
		},
	}
}

func TestValidateRecordShape_AcceptsCorrectShapes(t *testing.T) {
	arr := toolWithRecordInput("record:sequence_input[]")
	err := ValidateRecordShape(arr, map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/u/home/f.fasta", "type": "feature_group"},
		},
	})
	if err != nil {
		t.Errorf("valid record array rejected: %v", err)
	}

	single := toolWithRecordInput("record:sequence_input")
	err = ValidateRecordShape(single, map[string]any{
		"sequences": map[string]any{"filename": "/u/home/f.fasta", "type": "feature_group"},
	})
	if err != nil {
		t.Errorf("valid single record rejected: %v", err)
	}
}

// TestValidateRecordShape_RejectsArrayOfStrings is a regression guard.
//
// Checking only the container let ["/path/to/file"] through for a record[]
// input. It reached BV-BRC and died in Perl preflight with "Can't use string
// as a HASH ref" — 11 Gene Tree failures, none of them diagnosable from
// GoWe's side. Caught 2026-10-05 by submitting the exact payload.
func TestValidateRecordShape_RejectsArrayOfStrings(t *testing.T) {
	tool := toolWithRecordInput("record:sequence_input[]")
	err := ValidateRecordShape(tool, map[string]any{
		"sequences": []any{"/clark@patricbrc.org/home/alignment.afa"},
	})
	if err == nil {
		t.Fatal("array of strings accepted for a record[] input")
	}
	if !errors.Is(err, ErrInputValidation) {
		t.Errorf("error does not wrap ErrInputValidation: %v", err)
	}
	// The message must name the expected fields so the caller can self-correct.
	for _, want := range []string{"sequences[0]", "filename", "type"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

// TestValidateRecordShape_RejectsBareScalar is the MSA SNP failure mode: a
// bare workspace path where a record was declared. It matched neither case of
// the old type switch and fell through silently.
func TestValidateRecordShape_RejectsBareScalar(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"string", "/ARWattam@patricbrc.org/home/Feature Groups/Omp31"},
		{"number", 42.0},
		{"bool", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := toolWithRecordInput("record:sequence_input[]")
			err := ValidateRecordShape(tool, map[string]any{"sequences": tc.value})
			if err == nil {
				t.Fatalf("bare %s accepted for a record[] input", tc.name)
			}
			if !errors.Is(err, ErrInputValidation) {
				t.Errorf("error does not wrap ErrInputValidation: %v", err)
			}
		})
	}
}

func TestValidateRecordShape_ContainerMismatchStillCaught(t *testing.T) {
	// Single object where an array is declared.
	err := ValidateRecordShape(toolWithRecordInput("record:sequence_input[]"),
		map[string]any{"sequences": map[string]any{"filename": "f", "type": "t"}})
	if err == nil {
		t.Error("single object accepted for a record[] input")
	}
	// Array where a single record is declared.
	err = ValidateRecordShape(toolWithRecordInput("record:sequence_input"),
		map[string]any{"sequences": []any{map[string]any{"filename": "f", "type": "t"}}})
	if err == nil {
		t.Error("array accepted for a single-record input")
	}
}

func TestValidateRecordShape_IgnoresAbsentAndNonRecordInputs(t *testing.T) {
	tool := toolWithRecordInput("record:sequence_input[]")
	if err := ValidateRecordShape(tool, map[string]any{}); err != nil {
		t.Errorf("absent input should be ignored (required-ness is checked elsewhere): %v", err)
	}
	if err := ValidateRecordShape(tool, map[string]any{"sequences": nil}); err != nil {
		t.Errorf("nil input should be ignored: %v", err)
	}
	plain := &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{"name": {Type: "string"}},
	}
	if err := ValidateRecordShape(plain, map[string]any{"name": "fine"}); err != nil {
		t.Errorf("non-record input should be ignored: %v", err)
	}
}
