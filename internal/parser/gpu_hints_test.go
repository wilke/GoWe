package parser

import (
	"strings"
	"testing"
)

// gpuOf returns the merged step hints' (RequiresGPU, GPUs), (false, 0) for nil.
func gpuOf(t *testing.T, doc string) (bool, int, error) {
	t.Helper()
	h, err := modelStepHints(t, doc)
	if err != nil || h == nil {
		return false, 0, err
	}
	return h.RequiresGPU, h.GPUs, nil
}

func TestParser_GPUCount(t *testing.T) {
	tests := []struct {
		name      string
		toolHints string
		toolReqs  string
		stepHints string
		stepR     string
		want      int
	}{
		{name: "gpu true only, no DockerRequirement", toolHints: `      "gowe:Execution":
        gpu: true
`, want: 1},
		{name: "gpu_count", toolHints: `      "gowe:Execution":
        gpu_count: 3
`, want: 3},
		{name: "gpu_count wins over gpu", toolHints: `      "gowe:Execution":
        gpu: true
        gpu_count: 2
`, want: 2},
		{name: "gpu false", toolHints: `      "gowe:Execution":
        gpu: false
        worker_group: g
`, want: 0},
		{name: "CUDARequirement in hints defaults to 1", toolHints: `      "cwltool:CUDARequirement": {}
`, want: 1},
		{name: "CUDARequirement null body defaults to 1", toolHints: `      "cwltool:CUDARequirement":
`, want: 1},
		{name: "CUDARequirement in requirements with cudaDeviceCountMin", toolReqs: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 2
`, want: 2},
		{name: "CUDARequirement list form", toolReqs: `      - class: "cwltool:CUDARequirement"
        cudaDeviceCountMin: 2
`, want: 2},
		{name: "CUDARequirement full URI", toolReqs: `      "http://commonwl.org/cwltool#CUDARequirement":
        cudaDeviceCountMin: 2
`, want: 2},
		{name: "CUDARequirement ignored fields accepted", toolReqs: `      "cwltool:CUDARequirement":
        cudaVersionMin: "11.4"
        cudaComputeCapability: ["7.0", "8.0"]
        cudaDeviceCountMin: 2
        cudaDeviceCountMax: 4
`, want: 2},
		{name: "requirements win over hints", toolHints: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 4
`, toolReqs: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 2
`, want: 2},
		{name: "unmapped prefix is not CUDARequirement", toolHints: `      "foo:CUDARequirement":
        cudaDeviceCountMin: 2
`, want: 0},
		{name: "gowe and CUDA: CUDA larger", toolHints: `      "gowe:Execution":
        gpu_count: 2
      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 3
`, want: 3},
		{name: "gowe and CUDA: gowe larger", toolHints: `      "gowe:Execution":
        gpu_count: 4
`, toolReqs: `      "cwltool:CUDARequirement": {}
`, want: 4},
		{name: "step requirement raises the tool count", toolHints: `      "gowe:Execution":
        gpu_count: 1
`, stepR: `          "cwltool:CUDARequirement":
            cudaDeviceCountMin: 2
`, want: 2},
		{name: "step gpu: true does not lower the tool count", toolReqs: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 4
`, stepHints: `          "gowe:Execution":
            gpu: true
`, want: 4},
		{name: "step without GPU keeps the tool count", toolHints: `      "gowe:Execution":
        gpu_count: 2
`, stepHints: `          "gowe:Execution":
            worker_group: g
`, want: 2},
		{name: "step-only CUDARequirement", stepHints: `          "cwltool:CUDARequirement": {}
`, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, n, err := gpuOf(t, graphDoc(tt.toolHints, tt.toolReqs, tt.stepHints, tt.stepR))
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.want || req != (tt.want > 0) {
				t.Errorf("RequiresGPU=%v GPUs=%d, want %v/%d", req, n, tt.want > 0, tt.want)
			}
		})
	}
}

func TestParser_GPUCountErrors(t *testing.T) {
	tests := []struct {
		name      string
		toolHints string
		toolReqs  string
		stepR     string
		wantErr   string
	}{
		{name: "expression", toolReqs: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: $(inputs.n)
`, wantErr: "cudaDeviceCountMin must be an integer >= 1"},
		{name: "zero", toolHints: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 0
`, wantErr: "cudaDeviceCountMin must be an integer >= 1"},
		{name: "fraction", toolHints: `      "cwltool:CUDARequirement":
        cudaDeviceCountMin: 1.5
`, wantErr: "cudaDeviceCountMin must be an integer >= 1"},
		{name: "not a mapping", toolHints: `      "cwltool:CUDARequirement": 2
`, wantErr: "must be a mapping"},
		{name: "gpu_count zero", toolHints: `      "gowe:Execution":
        gpu_count: 0
`, wantErr: "gpu_count must be an integer >= 1"},
		{name: "gpu_count string", toolHints: `      "gowe:Execution":
        gpu_count: "2"
`, wantErr: "gpu_count must be an integer >= 1"},
		{name: "invalid step requirement", stepR: `          "cwltool:CUDARequirement":
            cudaDeviceCountMin: -1
`, wantErr: "cudaDeviceCountMin must be an integer >= 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := modelStepHints(t, graphDoc(tt.toolHints, tt.toolReqs, "", tt.stepR))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// A prefix that the document's $namespaces maps to the cwltool namespace
// spells CUDARequirement too.
func TestParser_GPUCountCustomPrefix(t *testing.T) {
	doc := "$namespaces:\n  cu: http://commonwl.org/cwltool#\n" + graphDoc(`      "cu:CUDARequirement":
        cudaDeviceCountMin: 2
`, "", "", "")
	req, n, err := gpuOf(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	if !req || n != 2 {
		t.Errorf("RequiresGPU=%v GPUs=%d, want true/2", req, n)
	}
}

// A bare tool whose only hint is gpu: true keeps it.
func TestParser_BareToolGPUOnlyHint(t *testing.T) {
	doc := `cwlVersion: v1.2
class: CommandLineTool
baseCommand: nvidia-smi
inputs: []
outputs: []
hints:
  "gowe:Execution":
    gpu: true
`
	p := testParser()
	graph, err := p.ParseGraph([]byte(doc))
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	mw, err := p.ToModel(graph, "w")
	if err != nil {
		t.Fatal(err)
	}
	h := mw.Steps[0].Hints
	if h == nil || !h.RequiresGPU || h.GPUs != 1 {
		t.Fatalf("step hints = %+v, want RequiresGPU and GPUs 1", h)
	}
}

func TestExtractStepHints_GPUOnly(t *testing.T) {
	h, err := extractStepHints(map[string]any{"gowe:Execution": map[string]any{"gpu": true}}, nil)
	if err != nil || h == nil {
		t.Fatalf("extractStepHints = (%v, %v), want hints", h, err)
	}
	if !h.RequiresGPU || h.GPUs != 1 {
		t.Errorf("hints = %+v, want RequiresGPU and GPUs 1", h)
	}
}

func TestPositiveIntValue(t *testing.T) {
	tests := []struct {
		in      any
		want    int
		wantErr bool
	}{
		{2, 2, false},
		{int64(3), 3, false},
		{uint64(4), 4, false},
		{float64(2), 2, false}, // JSON-decoded
		{2.5, 0, true},
		{0, 0, true},
		{-1, 0, true},
		{"2", 0, true},
		{"$(inputs.n)", 0, true},
		{true, 0, true},
		{nil, 0, true},
	}
	for _, tt := range tests {
		got, err := positiveIntValue(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("positiveIntValue(%#v) = (%d, %v), want (%d, err=%v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCwltoolClassKeys(t *testing.T) {
	got := cwltoolClassKeys(map[string]string{
		"z":       cwltoolNamespaceURI,
		"a":       cwltoolNamespaceURI,
		"cwltool": cwltoolNamespaceURI,
		"other":   "http://example.org#",
	}, "CUDARequirement")
	want := "cwltool:CUDARequirement,http://commonwl.org/cwltool#CUDARequirement,a:CUDARequirement,z:CUDARequirement"
	if strings.Join(got, ",") != want {
		t.Errorf("keys = %v, want %s", got, want)
	}
}
