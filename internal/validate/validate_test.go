package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/cwl"
)

func enumTool(typeSchema any) *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		ID: "tool",
		Inputs: map[string]cwl.ToolInputParam{
			"chunk_method": {
				Type:       "string",
				TypeSchema: typeSchema,
			},
		},
	}
}

var enumSchema = map[string]any{"type": "enum", "symbols": []any{"fixed", "semantic"}}

// TestToolInputs_NoTypeSchema_NeverTypeValidated pins the #273 contract: a
// task.Tool map (or a directly-built cwl.ToolInputParam) whose TypeSchema is
// nil — e.g. a worker round trip that lost the typeSchema key, or an old
// task predating this feature — is never type-validated, in ANY mode,
// including enforce. Only nil means skip; it must never be re-derived from
// the flattened Type string.
func TestToolInputs_NoTypeSchema_NeverTypeValidated(t *testing.T) {
	tool := enumTool(nil) // TypeSchema absent
	for _, mode := range []Mode{ModeOff, ModeWarn, ModeEnforce, ""} {
		t.Run(string(mode)+"/nil-schema", func(t *testing.T) {
			err := ToolInputs(tool, map[string]any{"chunk_method": "not-a-symbol"}, mode, nil)
			if err != nil {
				t.Errorf("ToolInputs() error = %v, want nil (TypeSchema is nil: never type-validated)", err)
			}
		})
	}
}

// TestToolInputs_ModeGating covers the mode contract for the NEW
// TypeSchema-level check, holding a declared TypeSchema constant: off skips
// entirely (no error, no way to observe it ran), warn logs and returns nil,
// enforce returns an error wrapping ErrInputValidation.
func TestToolInputs_ModeGating(t *testing.T) {
	tool := enumTool(enumSchema)
	badInputs := map[string]any{"chunk_method": "not-a-symbol"}

	t.Run("off: bad value accepted", func(t *testing.T) {
		if err := ToolInputs(tool, badInputs, ModeOff, nil); err != nil {
			t.Errorf("error = %v, want nil in off mode", err)
		}
	})

	t.Run("warn: bad value accepted (logged, not failed)", func(t *testing.T) {
		if err := ToolInputs(tool, badInputs, ModeWarn, nil); err != nil {
			t.Errorf("error = %v, want nil in warn mode", err)
		}
	})

	t.Run(`empty mode ("") behaves like warn`, func(t *testing.T) {
		if err := ToolInputs(tool, badInputs, "", nil); err != nil {
			t.Errorf("error = %v, want nil for the empty mode (Effective() -> warn)", err)
		}
	})

	t.Run("enforce: bad value rejected, wraps ErrInputValidation", func(t *testing.T) {
		err := ToolInputs(tool, badInputs, ModeEnforce, nil)
		if err == nil {
			t.Fatal("error = nil, want a validation error in enforce mode")
		}
		if !errors.Is(err, ErrInputValidation) {
			t.Errorf("error %v does not wrap ErrInputValidation", err)
		}
		if !strings.Contains(err.Error(), "chunk_method") {
			t.Errorf("error = %q, want it to name chunk_method", err.Error())
		}
	})

	t.Run("enforce: valid value accepted", func(t *testing.T) {
		if err := ToolInputs(tool, map[string]any{"chunk_method": "fixed"}, ModeEnforce, nil); err != nil {
			t.Errorf("error = %v, want nil for a valid enum value", err)
		}
	})
}

// TestToolInputs_ToolLevelNeverEchoesValue confirms the tool-level check
// runs with Options.ToolLevel semantics: the offending value itself never
// appears in the enforce-mode error message (only the field name and
// expected symbols do).
func TestToolInputs_ToolLevelNeverEchoesValue(t *testing.T) {
	tool := enumTool(enumSchema)
	err := ToolInputs(tool, map[string]any{"chunk_method": "super-secret-value"}, ModeEnforce, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("error = %q, must never echo the offending value at tool level", err.Error())
	}
}

// TestToolInputs_RequiredNullUnchangedAcrossModes: the pre-existing
// required/null check (independent of TypeSchema) must behave identically
// in every mode — mode only gates the NEW TypeSchema check.
func TestToolInputs_RequiredNullUnchangedAcrossModes(t *testing.T) {
	tool := &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			"required_field": {Type: "string"},
		},
	}
	for _, mode := range []Mode{ModeOff, ModeWarn, ModeEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			err := ToolInputs(tool, map[string]any{}, mode, nil)
			if err == nil {
				t.Fatal("error = nil, want the pre-existing missing-required-input error regardless of mode")
			}
			if !strings.Contains(err.Error(), "required_field") {
				t.Errorf("error = %v, want it to name required_field", err)
			}
		})
	}
}

// TestExpressionToolInputs_ModeGating mirrors TestToolInputs_ModeGating for
// ExpressionToolInputs.
func TestExpressionToolInputs_ModeGating(t *testing.T) {
	tool := &cwl.ExpressionTool{
		ID: "expr-tool",
		Inputs: map[string]cwl.ToolInputParam{
			"chunk_method": {Type: "string", TypeSchema: enumSchema},
		},
	}
	badInputs := map[string]any{"chunk_method": "not-a-symbol"}

	if err := ExpressionToolInputs(tool, badInputs, ModeWarn, nil); err != nil {
		t.Errorf("warn mode: error = %v, want nil", err)
	}
	err := ExpressionToolInputs(tool, badInputs, ModeEnforce, nil)
	if err == nil || !errors.Is(err, ErrInputValidation) {
		t.Errorf("enforce mode: error = %v, want it to wrap ErrInputValidation", err)
	}

	// A declared default plus an explicit null is never an error, in any
	// mode — for the pre-existing required/null check this only applies to
	// Type "Any" (unchanged legacy behavior, see ExpressionToolInputs); the
	// NEW TypeSchema check independently allows null-with-default for every
	// type (schemaAllowsNull / HasDefault in validateParam).
	toolWithDefault := &cwl.ExpressionTool{
		Inputs: map[string]cwl.ToolInputParam{
			"chunk_method": {Type: "Any", TypeSchema: enumSchema, Default: "fixed"},
		},
	}
	if err := ExpressionToolInputs(toolWithDefault, map[string]any{"chunk_method": nil}, ModeEnforce, nil); err != nil {
		t.Errorf("null-with-default: error = %v, want nil even in enforce mode", err)
	}
}
