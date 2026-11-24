package worker

import (
	"reflect"
	"testing"

	"github.com/me/gowe/pkg/model"
)

func TestParseNvidiaSMICSV(t *testing.T) {
	h200 := model.GPUInfo{Index: 0, UUID: "GPU-1a2b3c4d-0000-1111-2222-333344445555", Name: "NVIDIA H200 NVL", MemTotalMB: 143771}
	tests := []struct {
		name     string
		out      string
		want     []model.GPUInfo
		wantErrs int
	}{
		{"real line", "0, GPU-1a2b3c4d-0000-1111-2222-333344445555, NVIDIA H200 NVL, 143771\n", []model.GPUInfo{h200}, 0},
		{"blank lines ignored", "\n0, GPU-a, NVIDIA A100-SXM4-80GB, 81920\n\n1, GPU-b, NVIDIA A100-SXM4-80GB, 81920\n\n",
			[]model.GPUInfo{
				{Index: 0, UUID: "GPU-a", Name: "NVIDIA A100-SXM4-80GB", MemTotalMB: 81920},
				{Index: 1, UUID: "GPU-b", Name: "NVIDIA A100-SXM4-80GB", MemTotalMB: 81920},
			}, 0},
		{"name with spaces and comma", "3, GPU-c, NVIDIA GeForce RTX 4090, Founders Edition, 24564",
			[]model.GPUInfo{{Index: 3, UUID: "GPU-c", Name: "NVIDIA GeForce RTX 4090, Founders Edition", MemTotalMB: 24564}}, 0},
		{"CRLF line endings", "0, GPU-a, Tesla T4, 15360\r\n",
			[]model.GPUInfo{{Index: 0, UUID: "GPU-a", Name: "Tesla T4", MemTotalMB: 15360}}, 0},
		{"malformed line skipped", "garbage\n0, GPU-a, Tesla T4, 15360\n",
			[]model.GPUInfo{{Index: 0, UUID: "GPU-a", Name: "Tesla T4", MemTotalMB: 15360}}, 1},
		{"non-numeric memory skipped", "0, GPU-a, Tesla T4, [N/A]\n1, GPU-b, Tesla T4, 15360\n",
			[]model.GPUInfo{{Index: 1, UUID: "GPU-b", Name: "Tesla T4", MemTotalMB: 15360}}, 1},
		{"non-numeric index skipped", "x, GPU-a, Tesla T4, 15360\n", nil, 1},
		{"negative index skipped", "-1, GPU-a, Tesla T4, 15360\n", nil, 1},
		{"empty uuid skipped", "0, , Tesla T4, 15360\n", nil, 1},
		{"empty output", "", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := parseNvidiaSMICSV(tt.out)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("gpus = %+v, want %+v", got, tt.want)
			}
			if len(errs) != tt.wantErrs {
				t.Errorf("errors = %v, want %d", errs, tt.wantErrs)
			}
		})
	}
}

func TestFilterGPUsByID(t *testing.T) {
	all := []model.GPUInfo{
		{Index: 0, UUID: "GPU-0"},
		{Index: 1, UUID: "GPU-1"},
		{Index: 2, UUID: "GPU-2"},
		{Index: 3, UUID: "GPU-3"},
	}
	tests := []struct {
		name      string
		deviceIDs string
		want      []int
	}{
		{"empty keeps all", "", []int{0, 1, 2, 3}},
		{"single index", "0", []int{0}},
		{"two indices", "0,2", []int{0, 2}},
		{"spaces around indices", " 1 , 3 ", []int{1, 3}},
		{"index that does not exist", "7", []int{}},
		{"existing and missing index", "1,7", []int{1}},
		{"uuid", "GPU-2", []int{2}},
		{"index and uuid mixed", "0, GPU-3", []int{0, 3}},
		{"unknown uuid", "GPU-x", []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterGPUsByID(all, tt.deviceIDs)
			if got == nil {
				t.Fatal("filterGPUsByID returned nil, want a non-nil list")
			}
			indices := make([]int, 0, len(got))
			for _, g := range got {
				indices = append(indices, g.Index)
			}
			if !reflect.DeepEqual(indices, tt.want) {
				t.Errorf("indices = %v, want %v", indices, tt.want)
			}
		})
	}
}
