package cwltool

import (
	"math"
	"testing"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

func rrMap(m map[string]any) map[string]any { return map[string]any{"ResourceRequirement": m} }

func TestEvaluateResourceRequest(t *testing.T) {
	ijs := func(lib ...any) map[string]any {
		return map[string]any{"InlineJavascriptRequirement": map[string]any{"expressionLib": lib}}
	}
	withReqs := func(reqs map[string]any) *cwl.CommandLineTool {
		return &cwl.CommandLineTool{Requirements: reqs}
	}
	tests := []struct {
		name    string
		tool    *cwl.CommandLineTool
		job     map[string]any
		lib     []string
		want    ResourceRequest
		wantErr bool
	}{
		{name: "no RR", tool: &cwl.CommandLineTool{}},
		{name: "static ints", tool: withReqs(rrMap(map[string]any{"coresMin": 4, "ramMin": 2048})),
			want: ResourceRequest{4, 2048}},
		{name: "float ceils", tool: withReqs(rrMap(map[string]any{"coresMin": 2.2, "ramMin": 100.1})),
			want: ResourceRequest{3, 101}},
		{name: "hints only", tool: &cwl.CommandLineTool{Hints: rrMap(map[string]any{"coresMin": 2})},
			want: ResourceRequest{2, 0}},
		{name: "requirements win over hints", tool: &cwl.CommandLineTool{
			Requirements: rrMap(map[string]any{"coresMin": 8}), Hints: rrMap(map[string]any{"coresMin": 2})},
			want: ResourceRequest{8, 0}},
		{name: "coresMin expression", tool: withReqs(map[string]any{
			"InlineJavascriptRequirement": map[string]any{},
			"ResourceRequirement":         map[string]any{"coresMin": "$(inputs.threads)"}}),
			job: map[string]any{"threads": 16}, want: ResourceRequest{16, 0}},
		{name: "ramMin expression", tool: withReqs(rrMap(map[string]any{"ramMin": "$(inputs.ram_mb)"})),
			job: map[string]any{"ram_mb": 32000}, want: ResourceRequest{0, 32000}},
		{name: "cores key when coresMin absent", tool: withReqs(rrMap(map[string]any{"cores": 6})),
			want: ResourceRequest{6, 0}},
		{name: "cores overrides coresMin==1", tool: withReqs(rrMap(map[string]any{"coresMin": 1, "cores": 6})),
			want: ResourceRequest{6, 0}},
		{name: "coresMin>1 beats cores", tool: withReqs(rrMap(map[string]any{"coresMin": 3, "cores": 6})),
			want: ResourceRequest{3, 0}},
		{name: "tool default when job omits input", tool: &cwl.CommandLineTool{
			Inputs:       map[string]cwl.ToolInputParam{"threads": {Type: "int", Default: 8}},
			Requirements: rrMap(map[string]any{"coresMin": "$(inputs.threads)"})},
			job: map[string]any{}, want: ResourceRequest{8, 0}},
		{name: "tool default when job value null", tool: &cwl.CommandLineTool{
			Inputs:       map[string]cwl.ToolInputParam{"threads": {Type: "int", Default: 8}},
			Requirements: rrMap(map[string]any{"coresMin": "$(inputs.threads)"})},
			job: map[string]any{"threads": nil}, want: ResourceRequest{8, 0}},
		{name: "missing input", tool: withReqs(rrMap(map[string]any{"coresMin": "$(inputs.threads)"})),
			job: map[string]any{}, wantErr: true},
		{name: "file size unknown", tool: withReqs(rrMap(map[string]any{"ramMin": "$(inputs.f.size)"})),
			job: map[string]any{"f": map[string]any{"class": "File", "location": "file:///nonexistent/x"}}, wantErr: true},
		{name: "JS block with expressionLib", tool: withReqs(map[string]any{
			"InlineJavascriptRequirement": map[string]any{"expressionLib": []any{"function dbl(x){return x*2;}"}},
			"ResourceRequirement":         map[string]any{"coresMin": "${ return dbl(inputs.t); }"}}),
			job: map[string]any{"t": 5}, want: ResourceRequest{10, 0}},
		{name: "explicit lib wins", tool: withReqs(rrMap(map[string]any{"coresMin": "$(trip(inputs.t))"})),
			job: map[string]any{"t": 2}, lib: []string{"function trip(x){return x*3;}"}, want: ResourceRequest{6, 0}},
		{name: "literal non-expression string", tool: withReqs(rrMap(map[string]any{"coresMin": "four"})), wantErr: true},
		{name: "expression returns string", tool: withReqs(rrMap(map[string]any{"coresMin": "$(inputs.s)"})),
			job: map[string]any{"s": "abc"}, wantErr: true},
		{name: "ramMin fails coresMin kept", tool: withReqs(rrMap(map[string]any{"coresMin": 4, "ramMin": "$(inputs.nope)"})),
			job: map[string]any{}, want: ResourceRequest{4, 0}, wantErr: true},
		{name: "nil job", tool: withReqs(rrMap(map[string]any{"coresMin": 2, "ramMin": "$(inputs.x)"})),
			want: ResourceRequest{2, 0}, wantErr: true},
		{name: "leading space not evaluated", tool: withReqs(rrMap(map[string]any{"coresMin": " $(inputs.t)"})),
			job: map[string]any{"t": 4}, wantErr: true},
		{name: "nonpositive", tool: withReqs(rrMap(map[string]any{"coresMin": 0})), wantErr: true},
		{name: "ijs helper unused", tool: withReqs(ijs()), want: ResourceRequest{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateResourceRequest(tt.tool, tt.job, tt.lib)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEvaluateResourceRequest_DoesNotMutateJob(t *testing.T) {
	file := map[string]any{"class": "File", "path": "/tmp/some/reads.fq"}
	job := map[string]any{"f": file, "t": 4}
	tool := &cwl.CommandLineTool{Requirements: rrMap(map[string]any{"coresMin": "$(inputs.t)"})}
	if _, err := EvaluateResourceRequest(tool, job, nil); err != nil {
		t.Fatal(err)
	}
	if _, has := file["basename"]; has {
		t.Errorf("job File was mutated: %v", file)
	}
}

func TestEvaluateResourceRequest_AgreesWithResolveResources(t *testing.T) {
	tests := []struct {
		name string
		tool *cwl.CommandLineTool
		job  map[string]any
	}{
		{"static", &cwl.CommandLineTool{Requirements: rrMap(map[string]any{"coresMin": 4, "ramMin": 1024})}, nil},
		{"expr", &cwl.CommandLineTool{Requirements: rrMap(map[string]any{"coresMin": "$(inputs.t)", "ramMin": "$(inputs.t * 1000)"})}, map[string]any{"t": 7}},
		{"cores key", &cwl.CommandLineTool{Hints: rrMap(map[string]any{"cores": 5})}, nil},
		{"cores over 1", &cwl.CommandLineTool{Requirements: rrMap(map[string]any{"coresMin": 1, "cores": 9})}, nil},
		{"float", &cwl.CommandLineTool{Requirements: rrMap(map[string]any{"coresMin": 1.5, "ramMin": 10.2})}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := EvaluateResourceRequest(tt.tool, tt.job, nil)
			if err != nil {
				t.Fatal(err)
			}
			rt := BuildRuntimeContextWithInputs(tt.tool, "/tmp/out", tt.job, nil)
			if tt.job == nil {
				rt = BuildRuntimeContextWithInputs(tt.tool, "/tmp/out", map[string]any{}, nil)
			}
			// Undeclared dimensions are 0 here but carry the runtime default there.
			if got.Cores != rt.Cores || (got.RamMB != 0 && got.RamMB != rt.Ram) {
				t.Errorf("server %+v vs worker cores=%d ram=%d", got, rt.Cores, rt.Ram)
			}
		})
	}
}

func TestEvalResourceInt_FallbackToOneUnchanged(t *testing.T) {
	ev := cwlexpr.NewEvaluator(nil)
	ctx := cwlexpr.NewContext(map[string]any{"s": "abc"})
	tests := []struct {
		name  string
		value any
		ev    *cwlexpr.Evaluator
		want  int
	}{
		{"int", 4, nil, 4},
		{"zero stays zero", 0, nil, 0},
		{"negative stays", -3, nil, -3},
		{"float ceil", 2.2, nil, 3},
		{"literal string", "four", ev, 1},
		{"nil evaluator", "$(1+1)", nil, 1},
		{"expression ok", "$(2+3)", ev, 5},
		{"expression string result", "$(inputs.s)", ev, 1},
		{"expression error", "$(inputs.missing.x)", ev, 1},
		{"other type", true, nil, 1},
		{"nan", math.NaN(), nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c *cwlexpr.Context
			if tt.ev != nil {
				c = ctx
			}
			if tt.name == "nan" {
				// Strict path rejects NaN; legacy path must not panic.
				_ = evalResourceInt(tt.value, tt.ev, c)
				return
			}
			if got := evalResourceInt(tt.value, tt.ev, c); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
