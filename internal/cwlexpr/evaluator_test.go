package cwlexpr

import (
	"testing"
)

func TestEvaluator_ParameterReference(t *testing.T) {
	eval := NewEvaluator(nil)

	ctx := &Context{
		Inputs: map[string]any{
			"name":  "sample1",
			"count": 42,
			"file": map[string]any{
				"class":    "File",
				"path":     "/data/reads.fastq",
				"basename": "reads.fastq",
				"nameroot": "reads",
				"nameext":  ".fastq",
			},
		},
		Runtime: DefaultRuntimeContext(),
	}

	tests := []struct {
		name    string
		expr    string
		want    any
		wantErr bool
	}{
		{
			name: "simple string reference",
			expr: "$(inputs.name)",
			want: "sample1",
		},
		{
			name: "simple int reference",
			expr: "$(inputs.count)",
			want: int64(42),
		},
		{
			name: "nested property access",
			expr: "$(inputs.file.basename)",
			want: "reads.fastq",
		},
		{
			name: "string interpolation",
			expr: "output_$(inputs.name).txt",
			want: "output_sample1.txt",
		},
		{
			name: "multiple interpolations",
			expr: "$(inputs.name)_$(inputs.count)",
			want: "sample1_42",
		},
		{
			name: "arithmetic expression",
			expr: "$(inputs.count * 2)",
			want: int64(84),
		},
		{
			name: "literal string (no expression)",
			expr: "just a literal",
			want: "just a literal",
		},
		{
			name: "runtime reference",
			expr: "$(runtime.cores)",
			want: int64(1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval.Evaluate(tt.expr, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalValues(got, tt.want) {
				t.Errorf("Evaluate() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestEvaluator_CodeBlock(t *testing.T) {
	eval := NewEvaluator(nil)

	ctx := &Context{
		Inputs: map[string]any{
			"x": 10,
			"y": 20,
		},
		Runtime: DefaultRuntimeContext(),
	}

	tests := []struct {
		name    string
		expr    string
		want    any
		wantErr bool
	}{
		{
			name: "simple return",
			expr: "${ return inputs.x + inputs.y; }",
			want: int64(30),
		},
		{
			name: "conditional logic",
			expr: "${ if (inputs.x > 5) { return 'big'; } else { return 'small'; } }",
			want: "big",
		},
		{
			name: "array manipulation",
			expr: "${ return [inputs.x, inputs.y]; }",
			want: []any{int64(10), int64(20)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval.Evaluate(tt.expr, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalValues(got, tt.want) {
				t.Errorf("Evaluate() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestEvaluator_WithSelf(t *testing.T) {
	eval := NewEvaluator(nil)

	ctx := &Context{
		Inputs: map[string]any{"id": "sample"},
		Self: map[string]any{
			"class":    "File",
			"path":     "/output/result.txt",
			"basename": "result.txt",
		},
		Runtime: DefaultRuntimeContext(),
	}

	tests := []struct {
		name    string
		expr    string
		want    any
		wantErr bool
	}{
		{
			name: "self reference",
			expr: "$(self.basename)",
			want: "result.txt",
		},
		{
			name: "combine self and inputs",
			expr: "$(inputs.id)_$(self.basename)",
			want: "sample_result.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval.Evaluate(tt.expr, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("Evaluate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalValues(got, tt.want) {
				t.Errorf("Evaluate() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestEvaluator_WithExpressionLib(t *testing.T) {
	lib := []string{
		`function double(x) { return x * 2; }`,
		`function greet(name) { return "Hello, " + name; }`,
	}
	eval := NewEvaluator(lib)

	ctx := &Context{
		Inputs: map[string]any{
			"value": 21,
			"name":  "World",
		},
		Runtime: DefaultRuntimeContext(),
	}

	tests := []struct {
		name string
		expr string
		want any
	}{
		{
			name: "use library function",
			expr: "$(double(inputs.value))",
			want: int64(42),
		},
		{
			name: "use string function",
			expr: "$(greet(inputs.name))",
			want: "Hello, World",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval.Evaluate(tt.expr, ctx)
			if err != nil {
				t.Errorf("Evaluate() error = %v", err)
				return
			}
			if !equalValues(got, tt.want) {
				t.Errorf("Evaluate() = %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestEvaluateBool(t *testing.T) {
	eval := NewEvaluator(nil)
	ctx := &Context{
		Inputs:  map[string]any{"flag": true, "count": 5},
		Runtime: DefaultRuntimeContext(),
	}

	tests := []struct {
		name    string
		expr    string
		want    bool
		wantErr bool
	}{
		{
			name: "boolean input",
			expr: "$(inputs.flag)",
			want: true,
		},
		{
			name: "comparison",
			expr: "$(inputs.count > 3)",
			want: true,
		},
		{
			name: "code block bool",
			expr: "${ return inputs.count === 5; }",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := eval.EvaluateBool(tt.expr, ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("EvaluateBool() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("EvaluateBool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileObject(t *testing.T) {
	tests := []struct {
		path     string
		basename string
		dirname  string
		nameroot string
		nameext  string
	}{
		{
			path:     "/data/reads.fastq.gz",
			basename: "reads.fastq.gz",
			dirname:  "/data",
			nameroot: "reads.fastq",
			nameext:  ".gz",
		},
		{
			path:     "/home/user/sample.txt",
			basename: "sample.txt",
			dirname:  "/home/user",
			nameroot: "sample",
			nameext:  ".txt",
		},
		{
			path:     "file.txt",
			basename: "file.txt",
			dirname:  ".",
			nameroot: "file",
			nameext:  ".txt",
		},
		{
			path:     "/root/noext",
			basename: "noext",
			dirname:  "/root",
			nameroot: "noext",
			nameext:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			fo := NewFileObject(tt.path)
			if fo.Basename != tt.basename {
				t.Errorf("Basename = %q, want %q", fo.Basename, tt.basename)
			}
			if fo.Dirname != tt.dirname {
				t.Errorf("Dirname = %q, want %q", fo.Dirname, tt.dirname)
			}
			if fo.Nameroot != tt.nameroot {
				t.Errorf("Nameroot = %q, want %q", fo.Nameroot, tt.nameroot)
			}
			if fo.Nameext != tt.nameext {
				t.Errorf("Nameext = %q, want %q", fo.Nameext, tt.nameext)
			}
		})
	}
}

func TestIsExpression(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"$(inputs.x)", true},
		{"${return 1;}", true},
		{"literal", false},
		{"prefix_$(inputs.x)_suffix", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsExpression(tt.input); got != tt.want {
				t.Errorf("IsExpression(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// equalValues compares two values for equality, handling type differences.
func equalValues(a, b any) bool {
	// Handle nil
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Handle numeric types
	if isNumeric(a) && isNumeric(b) {
		return toFloat64(a) == toFloat64(b)
	}

	// Handle slices
	if sliceA, ok := a.([]any); ok {
		if sliceB, ok := b.([]any); ok {
			if len(sliceA) != len(sliceB) {
				return false
			}
			for i := range sliceA {
				if !equalValues(sliceA[i], sliceB[i]) {
					return false
				}
			}
			return true
		}
	}

	// Default comparison
	return a == b
}

func isNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	}
	return false
}

func toFloat64(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	}
	return 0
}
