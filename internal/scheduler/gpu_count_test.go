package scheduler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// gpuW returns an online GPU worker with an inventory of n GPUs (n < 0: not
// reported).
func gpuW(group string, n int) *model.Worker {
	w := capW(group, nil, nil)
	w.GPUEnabled = true
	if n >= 0 {
		w.GPUs = []model.GPUInfo{}
		for i := 0; i < n; i++ {
			w.GPUs = append(w.GPUs, model.GPUInfo{Index: i, UUID: fmt.Sprintf("GPU-%d", i)})
		}
	}
	return w
}

func TestCanMatchTask_GPUCount(t *testing.T) {
	tests := []struct {
		name       string
		caps       *WorkerCapabilities
		hints      *model.RuntimeHints
		want       bool
		wantReason string
	}{
		{"count fits capacity", capsOf(gpuW("", 2)), &model.RuntimeHints{RequiresGPU: true, GPUs: 2}, true, ""},
		{"count above every capacity", capsOf(gpuW("", 2), gpuW("", 1)), &model.RuntimeHints{RequiresGPU: true, GPUs: 3}, false,
			"can give a task 3 GPUs (largest 2"},
		{"one worker large enough", capsOf(gpuW("", 2), gpuW("", 4)), &model.RuntimeHints{RequiresGPU: true, GPUs: 3}, true, ""},
		{"unknown capacity takes one", capsOf(gpuW("", -1)), &model.RuntimeHints{RequiresGPU: true, GPUs: 1}, true, ""},
		{"unknown capacity boolean only", capsOf(gpuW("", -1)), &model.RuntimeHints{RequiresGPU: true}, true, ""},
		{"unknown capacity never two", capsOf(gpuW("", -1)), &model.RuntimeHints{RequiresGPU: true, GPUs: 2}, false,
			"can give a task 2 GPUs (largest 1"},
		{"empty inventory never two", capsOf(gpuW("", 0)), &model.RuntimeHints{RequiresGPU: true, GPUs: 2}, false,
			"can give a task 2 GPUs (largest 1"},
		{"no GPU workers", capsOf(capW("", nil, nil)), &model.RuntimeHints{RequiresGPU: true, GPUs: 1}, false,
			"no GPU-enabled workers"},
		{"count without boolean still needs a GPU worker", capsOf(capW("", nil, nil)), &model.RuntimeHints{GPUs: 1}, false,
			"no GPU-enabled workers"},
		{"CPU task on GPU worker", capsOf(gpuW("", 2)), &model.RuntimeHints{}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := canMatchTask(tt.caps, tt.hints)
			if ok != tt.want {
				t.Fatalf("ok = %v (reason %q), want %v", ok, reason, tt.want)
			}
			if tt.wantReason != "" && !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason %q missing %q", reason, tt.wantReason)
			}
		})
	}
}

func TestCreateTaskFromStep_GPUPropagation(t *testing.T) {
	tests := []struct {
		name      string
		hints     *model.StepHints
		wantReq   bool
		wantGPUs  int
		wantCount int
	}{
		{"count", &model.StepHints{RequiresGPU: true, GPUs: 2}, true, 2, 2},
		{"count without boolean syncs boolean", &model.StepHints{GPUs: 3}, true, 3, 3},
		{"boolean only (older workflow)", &model.StepHints{RequiresGPU: true}, true, 0, 1},
		{"no GPU", &model.StepHints{WorkerGroup: "g"}, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, _ := testSetup(t)
			si := &model.StepInstance{ID: "si_1", SubmissionID: "sub_1", StepID: "s"}
			tmp := &model.Task{RuntimeHints: &model.RuntimeHints{}}
			step := &model.Step{ID: "s", Hints: tt.hints}
			sub := &model.Submission{ID: "sub_1"}
			task := sched.createTaskFromStep(si, tmp, step, sub, model.ExecutorTypeWorker, -1)
			h := task.RuntimeHints
			if h == nil {
				h = &model.RuntimeHints{}
			}
			if h.RequiresGPU != tt.wantReq || h.GPUs != tt.wantGPUs || h.GPUCount() != tt.wantCount {
				t.Errorf("hints requires_gpu=%v gpus=%d count=%d, want %v/%d/%d",
					h.RequiresGPU, h.GPUs, h.GPUCount(), tt.wantReq, tt.wantGPUs, tt.wantCount)
			}
			if tmp.RuntimeHints.RequiresGPU || tmp.RuntimeHints.GPUs != 0 {
				t.Errorf("template hints were mutated: %+v", tmp.RuntimeHints)
			}
		})
	}
}
