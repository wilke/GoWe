package validate

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// --- testdata/schemas.json contract fixture -------------------------------

type validateCase struct {
	Name           string `json:"name"`
	Schema         any    `json:"schema"`
	Value          any    `json:"value"`
	Valid          bool   `json:"valid"`
	ErrContains    string `json:"errContains"`
	ErrPath        string `json:"errPath"`
	ErrNotContains string `json:"errNotContains"`
}

type paramCase struct {
	Name       string `json:"name"`
	Schema     any    `json:"schema"`
	HasDefault bool   `json:"hasDefault"`
	Present    bool   `json:"present"`
	Value      any    `json:"value"`
	Secret     bool   `json:"secret"`
	// CheckMissing mirrors Options.CheckMissing (#273 review #2): the
	// fixture's missing/null cases opt into the pre-#273 required/default/
	// nullable behaviour explicitly; every other case exercises the new
	// default (false), where a missing/null top-level value is never
	// reported.
	CheckMissing   bool   `json:"checkMissing"`
	Valid          bool   `json:"valid"`
	ErrContains    string `json:"errContains"`
	ErrPath        string `json:"errPath"`
	ErrNotContains string `json:"errNotContains"`
}

type schemasFixture struct {
	Validate []validateCase `json:"validate"`
	Params   []paramCase    `json:"params"`
}

func loadFixture(t *testing.T) schemasFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/schemas.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx schemasFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return fx
}

func anyMessageContains(errs []model.FieldError, s string) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, s) {
			return true
		}
	}
	return false
}

func anyPathEquals(errs []model.FieldError, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

func checkCommon(t *testing.T, errs []model.FieldError, valid bool, errContains, errPath, errNotContains string) {
	t.Helper()
	gotValid := len(errs) == 0
	if gotValid != valid {
		t.Fatalf("valid = %v, want %v (errors: %+v)", gotValid, valid, errs)
	}
	if errContains != "" && !anyMessageContains(errs, errContains) {
		t.Fatalf("expected an error message containing %q, got %+v", errContains, errs)
	}
	if errPath != "" && !anyPathEquals(errs, errPath) {
		t.Fatalf("expected an error at path %q, got %+v", errPath, errs)
	}
	if errNotContains != "" && anyMessageContains(errs, errNotContains) {
		t.Fatalf("expected no error message containing %q, got %+v", errNotContains, errs)
	}
}

func TestFixtureValidate(t *testing.T) {
	fx := loadFixture(t)
	if len(fx.Validate) == 0 {
		t.Fatal("fixture has no 'validate' cases")
	}
	for _, tc := range fx.Validate {
		t.Run(tc.Name, func(t *testing.T) {
			errs := ValidateValue(tc.Value, tc.Schema, "", Options{})
			checkCommon(t, errs, tc.Valid, tc.ErrContains, tc.ErrPath, tc.ErrNotContains)
		})
	}
}

func TestFixtureParams(t *testing.T) {
	fx := loadFixture(t)
	if len(fx.Params) == 0 {
		t.Fatal("fixture has no 'params' cases")
	}
	for _, tc := range fx.Params {
		t.Run(tc.Name, func(t *testing.T) {
			params := map[string]ParamSpec{
				"value": {TypeSchema: tc.Schema, HasDefault: tc.HasDefault, Secret: tc.Secret},
			}
			inputs := map[string]any{}
			if tc.Present {
				inputs["value"] = tc.Value
			}
			errs := ValidateInputs(params, inputs, Options{CheckMissing: tc.CheckMissing})
			checkCommon(t, errs, tc.Valid, tc.ErrContains, tc.ErrPath, tc.ErrNotContains)
		})
	}
}

// --- Extra Go-level tests the JSON fixture cannot express ------------------

func TestNumericKinds(t *testing.T) {
	cases := []struct {
		name   string
		schema any
		value  any
		valid  bool
	}{
		{"uint64 within long range", "long", uint64(9223372036854775800), true},
		{"uint64 overflows int", "int", uint64(4147483647), false},
		{"int64 from YAML-style decode", "long", int64(1234567890123), true},
		{"int64 fits int32", "int", int64(42), true},
		{"float32 valid for double", "double", float32(3.14), true},
		{"float32 fraction invalid for int", "int", float32(3.5), false},
		{"float32 integral valid for int", "int", float32(3.0), true},
		{"json.Number integral valid for int", "int", json.Number("42"), true},
		{"json.Number fractional invalid for int", "int", json.Number("42.5"), false},
		{"json.Number fractional valid for double", "double", json.Number("42.5"), true},
		{"json.Number beyond 2^53 valid for long", "long", json.Number("9223372036854775807"), true},
		{"json.Number beyond 2^53 exceeds int32", "int", json.Number("9223372036854775807"), false},
		{"bool is not numeric", "double", true, false},
		{"numeric string is not coerced", "double", "3.14", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateValue(tc.value, tc.schema, "", Options{})
			gotValid := len(errs) == 0
			if gotValid != tc.valid {
				t.Fatalf("valid = %v, want %v (errors: %+v)", gotValid, tc.valid, errs)
			}
		})
	}
}

func TestDepthCap(t *testing.T) {
	// Build a schema/value nested 100 levels deep: array of array of ... of "int".
	const levels = 100
	schema := any("int")
	value := any(1)
	for i := 0; i < levels; i++ {
		schema = map[string]any{"type": "array", "items": schema}
		value = []any{value}
	}
	errs := ValidateValue(value, schema, "", Options{})
	if len(errs) == 0 {
		t.Fatal("expected at least one error for 100-deep nesting")
	}
	if !anyMessageContains(errs, "nesting too deep") {
		t.Fatalf("expected a 'nesting too deep' error, got %+v", errs)
	}
}

func TestErrorCap(t *testing.T) {
	schema := map[string]any{"type": "array", "items": "int"}
	items := make([]any, 50)
	for i := range items {
		items[i] = "not-an-int"
	}
	errs := ValidateValue(items, schema, "", Options{})
	if len(errs) != defaultMaxErrors {
		t.Fatalf("expected exactly %d capped errors for 50 bad items, got %d", defaultMaxErrors, len(errs))
	}

	// A custom cap is honoured too.
	errs = ValidateValue(items, schema, "", Options{MaxErrors: 5})
	if len(errs) != 5 {
		t.Fatalf("expected exactly 5 capped errors, got %d", len(errs))
	}
}

func TestSymbolListTruncation(t *testing.T) {
	symbols := make([]any, 30)
	for i := range symbols {
		symbols[i] = string(rune('a' + i))
	}
	schema := map[string]any{"type": "enum", "symbols": symbols}
	errs := ValidateValue("does-not-match", schema, "", Options{})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %+v", errs)
	}
	if !strings.Contains(errs[0].Message, "…(10 more)") {
		t.Fatalf("expected symbol list truncated with '…(10 more)', got %q", errs[0].Message)
	}
}

func TestValueTruncationAndQuoting(t *testing.T) {
	long := strings.Repeat("x", 100)
	errs := ValidateValue(long, "int", "", Options{})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %+v", errs)
	}
	msg := errs[0].Message
	if strings.Contains(msg, strings.Repeat("x", 65)) {
		t.Fatalf("expected the embedded value to be truncated to 64 runes, got %q", msg)
	}
	if !strings.Contains(msg, `"`) {
		t.Fatalf("expected the embedded value to be %%q-quoted, got %q", msg)
	}
}

func TestValueNewlineQuoting(t *testing.T) {
	errs := ValidateValue("line1\nline2", "int", "", Options{})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %+v", errs)
	}
	msg := errs[0].Message
	if strings.Contains(msg, "\n") {
		t.Fatalf("expected no raw newline in the message (should be %%q-escaped), got %q", msg)
	}
	if !strings.Contains(msg, `\n`) {
		t.Fatalf("expected an escaped \\n in the message, got %q", msg)
	}
}

func TestToolLevelNeverEchoes(t *testing.T) {
	errs := ValidateValue("top-secret-value", "int", "", Options{ToolLevel: true})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %+v", errs)
	}
	if strings.Contains(errs[0].Message, "top-secret-value") {
		t.Fatalf("tool-level message leaked the value: %q", errs[0].Message)
	}

	// Also via ValidateInputs, without Secret set on the param - ToolLevel
	// alone must redact.
	params := map[string]ParamSpec{"p": {TypeSchema: "int"}}
	inputs := map[string]any{"p": "another-secret"}
	errs = ValidateInputs(params, inputs, Options{ToolLevel: true})
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %+v", errs)
	}
	if strings.Contains(errs[0].Message, "another-secret") {
		t.Fatalf("tool-level message leaked the value: %q", errs[0].Message)
	}
}

func TestDeterministicOrdering(t *testing.T) {
	params := map[string]ParamSpec{
		"zeta":  {TypeSchema: "int"},
		"alpha": {TypeSchema: "int"},
		"mu":    {TypeSchema: "int"},
	}
	inputs := map[string]any{
		"zeta":  "not-an-int",
		"alpha": "not-an-int",
		"mu":    "not-an-int",
	}
	var lastOrder []string
	for i := 0; i < 10; i++ {
		errs := ValidateInputs(params, inputs, Options{})
		if len(errs) != 3 {
			t.Fatalf("iteration %d: expected 3 errors, got %d: %+v", i, len(errs), errs)
		}
		order := []string{errs[0].Field, errs[1].Field, errs[2].Field}
		if lastOrder != nil {
			for j := range order {
				if order[j] != lastOrder[j] {
					t.Fatalf("iteration %d: non-deterministic order: got %v, previously %v", i, order, lastOrder)
				}
			}
		}
		lastOrder = order
	}
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if lastOrder[i] != want[i] {
			t.Fatalf("expected Field-sorted order %v, got %v", want, lastOrder)
		}
	}
}

func TestArrayIndexAndRecordFieldPathCombine(t *testing.T) {
	// path "[2].b" per the plan's documented path examples.
	schema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":   "record",
			"fields": []any{map[string]any{"name": "b", "type": "int"}},
		},
	}
	value := []any{
		map[string]any{"b": 1},
		map[string]any{"b": 2},
		map[string]any{"b": "not-an-int"},
	}
	errs := ValidateValue(value, schema, "", Options{})
	if !anyPathEquals(errs, "[2].b") {
		t.Fatalf("expected an error at path \"[2].b\", got %+v", errs)
	}
}

func TestOptionalEnumKeepsSymbolsInError(t *testing.T) {
	schema := []any{"null", map[string]any{"type": "enum", "symbols": []any{"#chunk/fixed", "#chunk/semantic"}}}
	tests := []struct {
		name  string
		value any
		valid bool
	}{
		{"null ok", nil, true},
		{"valid symbol", "semantic", true},
		{"bad symbol", "semantic_pooled_typo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateValue(tt.value, schema, "", Options{})
			if tt.valid {
				if len(errs) != 0 {
					t.Fatalf("want valid, got %v", errs)
				}
				return
			}
			if len(errs) != 1 || !strings.Contains(errs[0].Message, "fixed, semantic") {
				t.Fatalf("want one error listing the symbols, got %v", errs)
			}
		})
	}
}

func TestMultiMemberUnionErrorDescribesEnumSymbols(t *testing.T) {
	schema := []any{"int", map[string]any{"type": "enum", "symbols": []any{"a", "b"}}}
	errs := ValidateValue("c", schema, "", Options{})
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "enum{a, b}") {
		t.Fatalf("want union error naming enum symbols, got %v", errs)
	}
}

func TestSubmissionInputs(t *testing.T) {
	const wf = `cwlVersion: v1.2
class: Workflow
inputs:
  chunk_method:
    type:
      - "null"
      - type: enum
        symbols: [fixed, sentence, semantic]
  size: {type: int, default: 512}
  password: string
  flag: boolean
outputs: []
steps: []
`
	const tool = `cwlVersion: v1.2
class: CommandLineTool
baseCommand: echo
inputs:
  n: int
outputs: []
`
	tests := []struct {
		name      string
		cwl       string
		secret    []string
		inputs    map[string]any
		wantField []string
		notIn     string
	}{
		{"valid", wf, nil, map[string]any{"chunk_method": "semantic", "password": "x", "flag": true}, nil, ""},
		{"bad enum lists symbols", wf, nil, map[string]any{"chunk_method": "semantic_pooled_typo", "password": "x", "flag": true}, []string{"chunk_method"}, ""},
		{"null uses default", wf, nil, map[string]any{"size": nil, "password": "x", "flag": false}, nil, ""},
		// "flag" is missing (required, no default) but not reported: production
		// callers (like SubmissionInputs, called here with the default
		// Options{}) leave CheckMissing false, so a missing top-level value is
		// never reported by this package - see TestSubmissionInputs_MissingRequiredNotReportedByDefault
		// and TestValidateInputs_CheckMissingDefaultFalse below (#273 review #2).
		{"missing required and wrong type", wf, nil, map[string]any{"password": "x", "size": "big"}, []string{"size"}, ""},
		{"secret never echoed", wf, []string{"password"}, map[string]any{"password": 12345678901, "flag": true}, []string{"password"}, "12345678901"},
		{"bare tool is wrapped", tool, nil, map[string]any{"n": "three"}, []string{"n"}, ""},
		{"undeclared keys ignored", tool, nil, map[string]any{"n": 3, "cwl:requirements": []any{}}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, err := SubmissionInputs([]byte(tt.cwl), tt.secret, tt.inputs, Options{})
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			var got []string
			for _, e := range errs {
				got = append(got, e.Field)
				if tt.notIn != "" && strings.Contains(e.Message, tt.notIn) {
					t.Errorf("message leaks value: %q", e.Message)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.wantField, ",") {
				t.Fatalf("fields = %v, want %v (errors: %v)", got, tt.wantField, errs)
			}
			if tt.name == "bad enum lists symbols" && !strings.Contains(errs[0].Message, "fixed, sentence, semantic") {
				t.Errorf("message should list symbols: %q", errs[0].Message)
			}
		})
	}
	t.Run("unparseable CWL is an error, not a rejection", func(t *testing.T) {
		errs, err := SubmissionInputs([]byte("::: not yaml :::"), nil, nil, Options{})
		if err == nil || errs != nil {
			t.Fatalf("want parse error and no field errors, got errs=%v err=%v", errs, err)
		}
	})
}

// TestValidateInputs_CheckMissingDefaultFalse is the #273 review-#2
// regression test: Options{} (the zero value, i.e. CheckMissing=false,
// what every production caller passes) must report nothing for a missing
// required top-level input - production callers rely on their own,
// pre-existing required-input check for that and would otherwise get a
// duplicate error (review #6). Required fields missing from a *supplied*
// record are unaffected (still reported unconditionally).
func TestValidateInputs_CheckMissingDefaultFalse(t *testing.T) {
	params := map[string]ParamSpec{
		"required_no_default": {TypeSchema: "int"},
		"required_record": {TypeSchema: map[string]any{
			"type":   "record",
			"fields": []any{map[string]any{"name": "a", "type": "string"}},
		}},
	}

	t.Run("missing top-level input", func(t *testing.T) {
		errs := ValidateInputs(params, map[string]any{}, Options{})
		if len(errs) != 0 {
			t.Fatalf("want no errors for a missing required input under the default (CheckMissing=false), got %+v", errs)
		}
	})

	t.Run("explicit null top-level input", func(t *testing.T) {
		inputs := map[string]any{"required_no_default": nil}
		errs := ValidateInputs(params, inputs, Options{})
		if len(errs) != 0 {
			t.Fatalf("want no errors for an explicit-null required input under the default (CheckMissing=false), got %+v", errs)
		}
	})

	t.Run("but a missing field inside a SUPPLIED record is still reported", func(t *testing.T) {
		inputs := map[string]any{"required_record": map[string]any{}}
		errs := ValidateInputs(params, inputs, Options{})
		if len(errs) != 1 || errs[0].Path != ".a" {
			t.Fatalf("want exactly one error at path \".a\" for the record's own missing required field, got %+v", errs)
		}
	})

	t.Run("CheckMissing=true restores the required check", func(t *testing.T) {
		errs := ValidateInputs(params, map[string]any{}, Options{CheckMissing: true})
		if len(errs) != 2 {
			t.Fatalf("want 2 errors (both missing top-level inputs) with CheckMissing=true, got %+v", errs)
		}
	})
}
