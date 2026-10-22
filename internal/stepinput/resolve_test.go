package stepinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSource_WorkflowInput(t *testing.T) {
	workflowInputs := map[string]any{
		"message": "hello",
		"count":   42,
	}
	stepOutputs := map[string]map[string]any{}

	// Test workflow input resolution.
	val := ResolveSource("message", workflowInputs, stepOutputs)
	if val != "hello" {
		t.Errorf("expected 'hello', got %v", val)
	}

	val = ResolveSource("count", workflowInputs, stepOutputs)
	if val != 42 {
		t.Errorf("expected 42, got %v", val)
	}

	// Test missing input returns nil.
	val = ResolveSource("nonexistent", workflowInputs, stepOutputs)
	if val != nil {
		t.Errorf("expected nil, got %v", val)
	}

	// Test empty source returns nil.
	val = ResolveSource("", workflowInputs, stepOutputs)
	if val != nil {
		t.Errorf("expected nil, got %v", val)
	}
}

func TestResolveSource_StepOutput(t *testing.T) {
	workflowInputs := map[string]any{}
	stepOutputs := map[string]map[string]any{
		"step1": {
			"result": "output1",
			"count":  10,
		},
		"step2": {
			"data": map[string]any{"key": "value"},
		},
	}

	// Test step output resolution.
	val := ResolveSource("step1/result", workflowInputs, stepOutputs)
	if val != "output1" {
		t.Errorf("expected 'output1', got %v", val)
	}

	val = ResolveSource("step1/count", workflowInputs, stepOutputs)
	if val != 10 {
		t.Errorf("expected 10, got %v", val)
	}

	// Test nested output.
	val = ResolveSource("step2/data", workflowInputs, stepOutputs)
	m, ok := val.(map[string]any)
	if !ok || m["key"] != "value" {
		t.Errorf("expected map with key='value', got %v", val)
	}

	// Test missing step returns nil.
	val = ResolveSource("nonexistent/output", workflowInputs, stepOutputs)
	if val != nil {
		t.Errorf("expected nil for missing step, got %v", val)
	}

	// Test missing output from existing step returns nil.
	val = ResolveSource("step1/nonexistent", workflowInputs, stepOutputs)
	if val != nil {
		t.Errorf("expected nil for missing output, got %v", val)
	}
}

func TestResolveInputs_SingleSource(t *testing.T) {
	inputs := []InputDef{
		{ID: "msg", Sources: []string{"message"}},
		{ID: "num", Sources: []string{"count"}},
	}
	workflowInputs := map[string]any{
		"message": "hello world",
		"count":   100,
	}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resolved["msg"] != "hello world" {
		t.Errorf("expected 'hello world', got %v", resolved["msg"])
	}
	if resolved["num"] != 100 {
		t.Errorf("expected 100, got %v", resolved["num"])
	}
}

func TestResolveInputs_MultipleSources(t *testing.T) {
	inputs := []InputDef{
		{ID: "combined", Sources: []string{"a", "b", "c"}},
	}
	workflowInputs := map[string]any{
		"a": 1,
		"b": 2,
		"c": 3,
	}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	arr, ok := resolved["combined"].([]any)
	if !ok {
		t.Fatalf("expected array, got %T", resolved["combined"])
	}
	if len(arr) != 3 || arr[0] != 1 || arr[1] != 2 || arr[2] != 3 {
		t.Errorf("expected [1, 2, 3], got %v", arr)
	}
}

func TestResolveInputs_Default(t *testing.T) {
	inputs := []InputDef{
		{ID: "provided", Sources: []string{"value"}},
		{ID: "missing", Sources: []string{"nonexistent"}, Default: "fallback"},
		{ID: "no_source", Sources: nil, Default: 42},
	}
	workflowInputs := map[string]any{
		"value": "actual",
	}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resolved["provided"] != "actual" {
		t.Errorf("expected 'actual', got %v", resolved["provided"])
	}
	if resolved["missing"] != "fallback" {
		t.Errorf("expected 'fallback', got %v", resolved["missing"])
	}
	if resolved["no_source"] != 42 {
		t.Errorf("expected 42, got %v", resolved["no_source"])
	}
}

func TestResolveInputs_ValueFrom(t *testing.T) {
	inputs := []InputDef{
		{ID: "doubled", Sources: []string{"num"}, ValueFrom: "$(self * 2)"},
		{ID: "concat", Sources: []string{"str"}, ValueFrom: "$(self + ' world')"},
	}
	workflowInputs := map[string]any{
		"num": 21,
		"str": "hello",
	}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check for numeric equality regardless of Go type (goja may return int64 or float64).
	if doubled, ok := resolved["doubled"].(int64); ok {
		if doubled != 42 {
			t.Errorf("expected 42, got %v", doubled)
		}
	} else if doubledF, ok := resolved["doubled"].(float64); ok {
		if doubledF != 42 {
			t.Errorf("expected 42, got %v", doubledF)
		}
	} else {
		t.Errorf("expected numeric 42, got %v (%T)", resolved["doubled"], resolved["doubled"])
	}
	if resolved["concat"] != "hello world" {
		t.Errorf("expected 'hello world', got %v", resolved["concat"])
	}
}

func TestResolveInputs_ValueFromWithDefault(t *testing.T) {
	inputs := []InputDef{
		{ID: "val", Sources: []string{"missing"}, Default: 10, ValueFrom: "$(self + 5)"},
	}
	workflowInputs := map[string]any{}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Default (10) should be used, then valueFrom adds 5.
	// Check for numeric equality regardless of Go type.
	if val, ok := resolved["val"].(int64); ok {
		if val != 15 {
			t.Errorf("expected 15, got %v", val)
		}
	} else if valF, ok := resolved["val"].(float64); ok {
		if valF != 15 {
			t.Errorf("expected 15, got %v", valF)
		}
	} else {
		t.Errorf("expected numeric 15, got %v (%T)", resolved["val"], resolved["val"])
	}
}

func TestResolveInputs_StepChain(t *testing.T) {
	inputs := []InputDef{
		{ID: "step1_out", Sources: []string{"step1/result"}},
		{ID: "step2_out", Sources: []string{"step2/data"}},
	}
	workflowInputs := map[string]any{}
	stepOutputs := map[string]map[string]any{
		"step1": {"result": "from_step1"},
		"step2": {"data": "from_step2"},
	}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resolved["step1_out"] != "from_step1" {
		t.Errorf("expected 'from_step1', got %v", resolved["step1_out"])
	}
	if resolved["step2_out"] != "from_step2" {
		t.Errorf("expected 'from_step2', got %v", resolved["step2_out"])
	}
}

func TestApplyLoadContents(t *testing.T) {
	// Create a temp file to test loadContents.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "hello from file"
	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Test with File object.
	fileObj := map[string]any{
		"class": "File",
		"path":  testFile,
	}
	result := ApplyLoadContents(fileObj, "")
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}
	if resultMap["contents"] != testContent {
		t.Errorf("expected contents=%q, got %q", testContent, resultMap["contents"])
	}

	// Test with array of File objects.
	fileArr := []any{fileObj}
	result = ApplyLoadContents(fileArr, "")
	resultArr, ok := result.([]any)
	if !ok || len(resultArr) != 1 {
		t.Fatalf("expected array of length 1, got %T", result)
	}
	firstFile, ok := resultArr[0].(map[string]any)
	if !ok || firstFile["contents"] != testContent {
		t.Errorf("expected file with contents, got %v", resultArr[0])
	}

	// Test with non-File value (should pass through unchanged).
	strVal := ApplyLoadContents("just a string", "")
	if strVal != "just a string" {
		t.Errorf("expected string pass-through, got %v", strVal)
	}
}

func TestResolveDefaultValue_File(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "default.txt")
	if err := os.WriteFile(testFile, []byte("default content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Test File default with relative path resolved against cwlDir.
	fileDefault := map[string]any{
		"class":    "File",
		"location": "default.txt",
	}
	resolved := ResolveDefaultValue(fileDefault, tmpDir)
	m, ok := resolved.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", resolved)
	}
	expectedPath := filepath.Join(tmpDir, "default.txt")
	if m["path"] != expectedPath && m["location"] != expectedPath {
		t.Errorf("expected path=%q, got path=%v, location=%v", expectedPath, m["path"], m["location"])
	}

	// Test nested array of Files.
	arrDefault := []any{fileDefault}
	resolved = ResolveDefaultValue(arrDefault, tmpDir)
	arr, ok := resolved.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected array of length 1, got %v", resolved)
	}
}

func TestInputDefFromModel(t *testing.T) {
	// Test with Sources array.
	def := InputDefFromModel("in1", []string{"a", "b"}, "ignored", "default", "$(self)", true, "", "")
	if def.ID != "in1" {
		t.Errorf("expected ID='in1', got %q", def.ID)
	}
	if len(def.Sources) != 2 || def.Sources[0] != "a" || def.Sources[1] != "b" {
		t.Errorf("expected Sources=[a,b], got %v", def.Sources)
	}
	if def.Default != "default" {
		t.Errorf("expected Default='default', got %v", def.Default)
	}
	if def.ValueFrom != "$(self)" {
		t.Errorf("expected ValueFrom='$(self)', got %q", def.ValueFrom)
	}
	if !def.LoadContents {
		t.Error("expected LoadContents=true")
	}

	// Test with comma-separated Source (backwards compat).
	def = InputDefFromModel("in2", nil, "x,y,z", nil, "", false, "", "")
	if len(def.Sources) != 3 || def.Sources[0] != "x" || def.Sources[1] != "y" || def.Sources[2] != "z" {
		t.Errorf("expected Sources=[x,y,z], got %v", def.Sources)
	}

	// Test with single Source.
	def = InputDefFromModel("in3", nil, "single", nil, "", false, "", "")
	if len(def.Sources) != 1 || def.Sources[0] != "single" {
		t.Errorf("expected Sources=[single], got %v", def.Sources)
	}

	// Test with empty Sources and Source.
	def = InputDefFromModel("in4", nil, "", "fallback", "", false, "", "")
	if len(def.Sources) != 0 {
		t.Errorf("expected empty Sources, got %v", def.Sources)
	}
	if def.Default != "fallback" {
		t.Errorf("expected Default='fallback', got %v", def.Default)
	}
}

func TestResolveInputs_LoadContentsBeforeValueFrom(t *testing.T) {
	// Create a temp file.
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("file contents"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	inputs := []InputDef{
		{
			ID:           "file_data",
			Sources:      []string{"input_file"},
			LoadContents: true,
			ValueFrom:    "$(self.contents)",
		},
	}
	workflowInputs := map[string]any{
		"input_file": map[string]any{
			"class": "File",
			"path":  testFile,
		},
	}
	stepOutputs := map[string]map[string]any{}

	resolved, err := ResolveInputs(inputs, workflowInputs, stepOutputs, Options{
		CWLDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// valueFrom should have access to self.contents from loadContents.
	if resolved["file_data"] != "file contents" {
		t.Errorf("expected 'file contents', got %v", resolved["file_data"])
	}
}

// TestResolveInputs_PickValue covers GoWe issue #197: step-level pickValue
// must be honored by ResolveInputs, mirroring the workflow-output pickValue
// semantics in internal/cwloutput. It must be applied before defaults and
// before any scatter split (the resolved value here is what scatter reads
// directly from Task.Job).
func TestResolveInputs_PickValue(t *testing.T) {
	tests := []struct {
		name           string
		inputs         []InputDef
		workflowInputs map[string]any
		wantValue      any
		wantErrSubstr  string
	}{
		{
			name: "first_non_null over multi-source picks first non-null",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "first_non_null"},
			},
			workflowInputs: map[string]any{"b": "from-b"}, // "a" resolves to nil (absent)
			wantValue:      "from-b",
		},
		{
			name: "first_non_null prefers the earlier non-null source",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "first_non_null"},
			},
			workflowInputs: map[string]any{"a": "from-a", "b": "from-b"},
			wantValue:      "from-a",
		},
		{
			name: "the_only_non_null with exactly one non-null value",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "the_only_non_null"},
			},
			workflowInputs: map[string]any{"b": "from-b"},
			wantValue:      "from-b",
		},
		{
			name: "the_only_non_null errors on multiple non-null values",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "the_only_non_null"},
			},
			workflowInputs: map[string]any{"a": "from-a", "b": "from-b"},
			wantErrSubstr:  "multiple non-null values",
		},
		{
			name: "all_non_null returns the non-null subset",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b", "c"}, PickValue: "all_non_null"},
			},
			workflowInputs: map[string]any{"b": 2, "c": 3},
			wantValue:      []any{2, 3},
		},
		{
			name: "all sources null with first_non_null errors",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "first_non_null"},
			},
			workflowInputs: map[string]any{},
			wantErrSubstr:  "all sources are null",
		},
		{
			name: "single source whose resolved value is an array is picked across",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"arr"}, PickValue: "first_non_null"},
			},
			workflowInputs: map[string]any{"arr": []any{nil, "x"}},
			wantValue:      "x",
		},
		{
			name: "single source non-array value with pickValue is a no-op pass-through",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"arr"}, PickValue: "first_non_null"},
			},
			workflowInputs: map[string]any{"arr": "solo"},
			wantValue:      "solo",
		},
		{
			name: "pickValue combines with merge_flattened linkMerge",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, LinkMerge: "merge_flattened", PickValue: "all_non_null"},
			},
			workflowInputs: map[string]any{"a": []any{1, 2}}, // "b" resolves to nil
			wantValue:      []any{1, 2},
		},
		{
			name: "default does not override an empty all_non_null result",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, PickValue: "all_non_null", Default: "fallback"},
			},
			workflowInputs: map[string]any{},
			wantValue:      []any{},
		},
		{
			// Multi-source resolution always yields a typed []any, even when
			// every element is nil, so the interface itself is never a true
			// nil — the default fallback (which checks `value == nil`) never
			// fires for a multi-source input, pickValue or not. This is
			// pre-existing behavior, unaffected by the pickValue change.
			name: "multi-source nil values do not trigger default",
			inputs: []InputDef{
				{ID: "picked", Sources: []string{"a", "b"}, Default: "fallback"},
			},
			workflowInputs: map[string]any{},
			wantValue:      []any{nil, nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := ResolveInputs(tt.inputs, tt.workflowInputs, map[string]map[string]any{}, Options{})
			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (resolved=%v)", tt.wantErrSubstr, resolved)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := resolved["picked"]
			if !valuesEqual(got, tt.wantValue) {
				t.Errorf("resolved[picked] = %#v, want %#v", got, tt.wantValue)
			}
		})
	}
}

func valuesEqual(a, b any) bool {
	aArr, aOk := a.([]any)
	bArr, bOk := b.([]any)
	if aOk != bOk {
		return false
	}
	if aOk {
		if len(aArr) != len(bArr) {
			return false
		}
		for i := range aArr {
			if !valuesEqual(aArr[i], bArr[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}
