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

// --- record-field enums -----------------------------------------------------
//
// These were entirely unvalidated until 2026-10-07: ValidateEnumValues walked
// only tool.Inputs, leaving 47 of the catalog's 298 declared values unchecked.
// The GeneTree shape below is the real one, and the failing value is the one an
// agent actually produced (an *aligned* FASTA labelled "feature_dna_fasta",
// which validated, reached BV-BRC and failed inside the app).

func geneTreeLikeTool() *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			"sequences": {
				Type: "record:sequence_input[]",
				RecordFields: []cwl.RecordField{
					{Name: "filename", Type: "string"},
					{
						Name: "type",
						Type: "string",
						Doc:  "What the path is [enum: feature_group, aligned_dna_fasta, feature_dna_fasta] [bvbrc:enum]",
						Symbols: []string{
							"feature_group", "aligned_dna_fasta", "feature_dna_fasta",
						},
					},
				},
			},
			// A single record, not an array — Fastq Utils and two others.
			"paired_end_libs": {
				Type: "record:paired_end_lib?",
				RecordFields: []cwl.RecordField{
					{Name: "read1", Type: "string"},
					{Name: "platform", Type: "string?", Symbols: []string{"infer", "illumina"}},
				},
			},
		},
	}
}

func TestValidateEnumValues_RecordFieldRejected(t *testing.T) {
	err := ValidateEnumValues(geneTreeLikeTool(), map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/x/y.afa", "type": "TOTALLY_BOGUS_TYPE"},
		},
	})
	if err == nil {
		t.Fatal("expected a rejection for an undeclared record-field enum value")
	}
	// The message must name the field path, the offending value AND the
	// permitted set, or an LLM cannot correct itself from it.
	for _, want := range []string{"sequences[0].type", "TOTALLY_BOGUS_TYPE", "aligned_dna_fasta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if !errors.Is(err, ErrInputValidation) {
		t.Errorf("error should wrap ErrInputValidation, got %v", err)
	}
}

func TestValidateEnumValues_RecordFieldIndexReported(t *testing.T) {
	err := ValidateEnumValues(geneTreeLikeTool(), map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/a.afa", "type": "feature_group"},
			map[string]any{"filename": "/b.afa", "type": "nope"},
		},
	})
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(err.Error(), "sequences[1].type") {
		t.Errorf("error should point at index 1, got %q", err.Error())
	}
}

func TestValidateEnumValues_SingleRecordNotArray(t *testing.T) {
	err := ValidateEnumValues(geneTreeLikeTool(), map[string]any{
		"paired_end_libs": map[string]any{"read1": "/r1.fq", "platform": "bogus"},
	})
	if err == nil {
		t.Fatal("expected a rejection for a single (non-array) record")
	}
	if !strings.Contains(err.Error(), "paired_end_libs.platform") {
		t.Errorf("error should name the field without an index, got %q", err.Error())
	}
}

func TestValidateEnumValues_RecordFieldAccepted(t *testing.T) {
	for _, v := range []string{"feature_group", "aligned_dna_fasta", "feature_dna_fasta"} {
		err := ValidateEnumValues(geneTreeLikeTool(), map[string]any{
			"sequences": []any{map[string]any{"filename": "/x.afa", "type": v}},
		})
		if err != nil {
			t.Errorf("declared value %q was rejected: %v", v, err)
		}
	}
}

func TestValidateEnumValues_RecordFieldSkips(t *testing.T) {
	cases := map[string]map[string]any{
		"field absent":        {"sequences": []any{map[string]any{"filename": "/x.afa"}}},
		"field nil":           {"sequences": []any{map[string]any{"type": nil}}},
		"non-string value":    {"sequences": []any{map[string]any{"type": 42}}},
		"element not record":  {"sequences": []any{"/just/a/path"}},
		"input absent":        {},
		"field has no symbol": {"paired_end_libs": map[string]any{"read1": "anything at all"}},
	}
	for name, inputs := range cases {
		if err := ValidateEnumValues(geneTreeLikeTool(), inputs); err != nil {
			t.Errorf("%s: should be skipped, got %v", name, err)
		}
	}
}

// --- required record fields -------------------------------------------------
//
// ValidateRecordFields catches a field that should not be there; these cover
// the opposite, which nothing checked until 2026-10-07. A half-built record
// validated and then failed inside the BV-BRC app.

func requiredFieldTool() *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			// Gene Tree's real shape: both fields required, no defaults.
			"sequences": {
				Type: "record:sequence_input[]",
				RecordFields: []cwl.RecordField{
					{Name: "filename", Type: "string"},
					{Name: "type", Type: "string"},
				},
			},
			// A single record mixing required, nullable and defaulted fields.
			"paired_end_libs": {
				Type: "record:paired_end_lib?",
				RecordFields: []cwl.RecordField{
					{Name: "read1", Type: "string"},
					{Name: "read2", Type: "string?"},
					{Name: "platform", Type: "string", Default: "infer"},
				},
			},
		},
	}
}

func TestValidateRecordRequiredFields_MissingRejected(t *testing.T) {
	// The exact payload that was accepted before this check existed.
	err := ValidateRecordRequiredFields(requiredFieldTool(), map[string]any{
		"sequences": []any{map[string]any{"type": "feature_group"}},
	})
	if err == nil {
		t.Fatal("a record missing a required field was accepted")
	}
	for _, want := range []string{"sequences[0]", "filename"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if !errors.Is(err, ErrInputValidation) {
		t.Errorf("should wrap ErrInputValidation, got %v", err)
	}
}

func TestValidateRecordRequiredFields_NullCountsAsMissing(t *testing.T) {
	err := ValidateRecordRequiredFields(requiredFieldTool(), map[string]any{
		"sequences": []any{map[string]any{"filename": nil, "type": "feature_group"}},
	})
	if err == nil {
		t.Fatal("an explicit null in a required field was accepted; the app sees nothing either way")
	}
}

func TestValidateRecordRequiredFields_IndexReported(t *testing.T) {
	err := ValidateRecordRequiredFields(requiredFieldTool(), map[string]any{
		"sequences": []any{
			map[string]any{"filename": "/a.afa", "type": "feature_group"},
			map[string]any{"filename": "/b.afa"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "sequences[1]") {
		t.Fatalf("should point at index 1, got %v", err)
	}
}

func TestValidateRecordRequiredFields_Accepts(t *testing.T) {
	cases := map[string]map[string]any{
		"all required present": {
			"sequences": []any{map[string]any{"filename": "/a.afa", "type": "feature_group"}},
		},
		"nullable field omitted": {
			"paired_end_libs": map[string]any{"read1": "/r1.fq", "platform": "illumina"},
		},
		"input absent":        {},
		"input nil":           {"sequences": nil},
		"element not record":  {"sequences": []any{"/just/a/path"}},
		"extra field present": {"sequences": []any{map[string]any{"filename": "/a", "type": "t", "zzz": 1}}},
	}
	for name, inputs := range cases {
		if err := ValidateRecordRequiredFields(requiredFieldTool(), inputs); err != nil {
			t.Errorf("%s: should be accepted here, got %v", name, err)
		}
	}
}

// A field with a declared default must not be reported missing once
// ApplyRecordFieldDefaults has run -- which is why the submission path calls
// them in that order.
func TestValidateRecordRequiredFields_DefaultFilledFirst(t *testing.T) {
	tool := requiredFieldTool()
	inputs := map[string]any{"paired_end_libs": map[string]any{"read1": "/r1.fq"}}
	ApplyRecordFieldDefaults(tool, inputs)
	if err := ValidateRecordRequiredFields(tool, inputs); err != nil {
		t.Errorf("platform has a default and should have been filled: %v", err)
	}
}

// --- plain array shape ------------------------------------------------------
//
// ValidateRecordShape covers arrays of records; these cover the plain ones,
// which nothing checked until 2026-10-07. The MSA SNP case below is the exact
// payload behind all 11 of its failures.

func arrayShapeTool() *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			"feature_groups": {Type: "string[]?"},
			"genome_ids":     {Type: "string[]?"},
			"bootstrap":      {Type: "int?"},
			"alphabet":       {Type: "string"},
			// An array of records belongs to ValidateRecordShape, which gives
			// a better message; this check must leave it alone.
			"sequences": {
				Type:         "record:sequence_input[]",
				RecordFields: []cwl.RecordField{{Name: "filename", Type: "string"}},
			},
		},
	}
}

func TestValidateArrayShape_RejectsTheMSASNPFailure(t *testing.T) {
	// All 11 MSA SNP failures passed feature_groups as a bare string; the one
	// success passed a one-element list.
	err := ValidateArrayShape(arrayShapeTool(), map[string]any{
		"feature_groups": "/clark.cucinell@patricbrc.org/home/Feature Groups/rpoB_feature_group_20",
	})
	if err == nil {
		t.Fatal("a bare string where string[]? is declared was accepted")
	}
	// The message must name the input and show the corrected shape, so an LLM
	// can fix it without re-reading the schema.
	for _, want := range []string{"feature_groups", "expects an array", "wrap it in a list", "rpoB_feature_group_20"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err.Error(), want)
		}
	}
	if !errors.Is(err, ErrInputValidation) {
		t.Errorf("should wrap ErrInputValidation, got %v", err)
	}
}

func TestValidateArrayShape_RejectsObjectAndNumber(t *testing.T) {
	if err := ValidateArrayShape(arrayShapeTool(), map[string]any{
		"genome_ids": map[string]any{"id": "1313.1"},
	}); err == nil || !strings.Contains(err.Error(), "got an object") {
		t.Errorf("an object where an array is declared should be refused, got %v", err)
	}
	if err := ValidateArrayShape(arrayShapeTool(), map[string]any{
		"genome_ids": 1313.1,
	}); err == nil {
		t.Error("a number where an array is declared should be refused")
	}
}

func TestValidateArrayShape_Accepts(t *testing.T) {
	cases := map[string]map[string]any{
		"proper list":           {"feature_groups": []any{"/a", "/b"}},
		"one-element list":      {"feature_groups": []any{"/a"}},
		"empty list":            {"feature_groups": []any{}},
		"absent":                {},
		"nil":                   {"feature_groups": nil},
		"scalar on scalar type": {"alphabet": "dna", "bootstrap": 100},
		// Left to ValidateRecordShape, which names the expected fields.
		"record array given a string": {"sequences": "/path/to/file"},
	}
	for name, inputs := range cases {
		if err := ValidateArrayShape(arrayShapeTool(), inputs); err != nil {
			t.Errorf("%s: should be accepted here, got %v", name, err)
		}
	}
}

// The two checks must not both fire on the same input, or the caller gets the
// less useful message.
func TestValidateArrayShape_DefersToRecordShape(t *testing.T) {
	inputs := map[string]any{"sequences": "/path/to/file"}
	if err := ValidateArrayShape(arrayShapeTool(), inputs); err != nil {
		t.Fatalf("ValidateArrayShape should ignore record arrays: %v", err)
	}
	if err := ValidateRecordShape(arrayShapeTool(), inputs); err == nil {
		t.Error("ValidateRecordShape should be the one to refuse it")
	}
}

// --- declared-type warnings --------------------------------------------------
//
// NOT a gate. A strict version was replayed against every COMPLETED submission
// before being wired in and refused 22 of them, so these report only. See
// InputTypeWarnings for the measurements.

func typeWarnTool() *cwl.CommandLineTool {
	return &cwl.CommandLineTool{
		Inputs: map[string]cwl.ToolInputParam{
			"bootstrap":   {Type: "int?"},
			"p_value":     {Type: "float?"},
			"trim":        {Type: "boolean?"},
			"genome_size": {Type: "string?"},
			"srr_libs": {
				Type: "record:srr_lib[]?",
				RecordFields: []cwl.RecordField{
					{Name: "srr_accession", Type: "string"},
					{Name: "condition", Type: "int?"},
				},
			},
		},
	}
}

func TestInputTypeWarnings_FlagsTheRealBugs(t *testing.T) {
	// condition is an INDEX into experimental_conditions, so a label silently
	// breaks DESeq grouping. A prepared payload of ours had exactly this.
	w := InputTypeWarnings(typeWarnTool(), map[string]any{
		"srr_libs": []any{map[string]any{"srr_accession": "SRR1", "condition": "Cd0"}},
	})
	if len(w) != 1 || !strings.Contains(w[0], "srr_libs[0].condition") {
		t.Fatalf("expected a warning naming the field, got %v", w)
	}
	if !strings.Contains(w[0], `"Cd0"`) {
		t.Errorf("warning should quote the offending value: %v", w)
	}
}

func TestInputTypeWarnings_Cases(t *testing.T) {
	for name, tc := range map[string]struct {
		inputs map[string]any
		want   int
	}{
		"non-numeric string for int":   {map[string]any{"bootstrap": "not-a-number"}, 1},
		"list where scalar declared":   {map[string]any{"bootstrap": []any{1, 2}}, 1},
		"object where scalar":          {map[string]any{"bootstrap": map[string]any{"a": 1}}, 1},
		"non-integral for int":         {map[string]any{"bootstrap": 1.5}, 1},
		"boolean for int":              {map[string]any{"bootstrap": true}, 1},
		"non-numeric string for float": {map[string]any{"p_value": "high"}, 1},
		"non-bool string for boolean":  {map[string]any{"trim": "yes please"}, 1},

		// Coercions real submissions rely on -- must NOT warn.
		"number for a string input":   {map[string]any{"genome_size": 4600000}, 0},
		"human size for string input": {map[string]any{"genome_size": "5M"}, 0},
		"numeric string for int":      {map[string]any{"bootstrap": "100"}, 0},
		"integral float for int":      {map[string]any{"bootstrap": 100.0}, 0},
		"bool string for boolean":     {map[string]any{"trim": "true"}, 0},
		"proper types":                {map[string]any{"bootstrap": 100.0, "p_value": 0.05, "trim": true}, 0},
		"absent":                      {map[string]any{}, 0},
		"explicit null":               {map[string]any{"bootstrap": nil}, 0},
	} {
		got := InputTypeWarnings(typeWarnTool(), tc.inputs)
		if len(got) != tc.want {
			t.Errorf("%s: want %d warning(s), got %d: %v", name, tc.want, len(got), got)
		}
	}
}
