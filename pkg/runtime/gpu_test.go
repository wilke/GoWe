package runtime

import (
	"context"
	"strings"
	"testing"
)

func TestGPUConfigHelpers(t *testing.T) {
	tests := []struct {
		name   string
		g      GPUConfig
		access bool
		env    string // CUDAEnv joined by "|"
		docker string // DockerGPUArgs joined by " "
	}{
		{"no GPU", GPUConfig{}, false, "", ""},
		{"DeviceID without Enabled is ignored", GPUConfig{DeviceID: "0"}, false, "", ""},
		{"legacy all", GPUConfig{Enabled: true}, true, "", "--gpus all"},
		{"legacy list", GPUConfig{Enabled: true, DeviceID: "0,1"}, true,
			"CUDA_VISIBLE_DEVICES=0,1|CUDA_DEVICE_ORDER=PCI_BUS_ID", `--gpus "device=0,1"`},
		{"assigned", GPUConfig{Devices: []string{"GPU-a", "GPU-b"}}, true,
			"CUDA_VISIBLE_DEVICES=GPU-a,GPU-b|CUDA_DEVICE_ORDER=PCI_BUS_ID", `--gpus "device=GPU-a,GPU-b"`},
		{"assigned overrides legacy id", GPUConfig{Enabled: true, DeviceID: "3", Devices: []string{"1"}}, true,
			"CUDA_VISIBLE_DEVICES=1|CUDA_DEVICE_ORDER=PCI_BUS_ID", `--gpus "device=1"`},
		{"denied", GPUConfig{AccessDenied: true}, false, "CUDA_VISIBLE_DEVICES=", ""},
		{"denied overrides everything", GPUConfig{Enabled: true, DeviceID: "0", Devices: []string{"GPU-a"}, AccessDenied: true}, false,
			"CUDA_VISIBLE_DEVICES=", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.g.HasAccess(); got != tt.access {
				t.Errorf("HasAccess = %v, want %v", got, tt.access)
			}
			if got := strings.Join(tt.g.CUDAEnv(), "|"); got != tt.env {
				t.Errorf("CUDAEnv = %q, want %q", got, tt.env)
			}
			if got := strings.Join(tt.g.DockerGPUArgs(), " "); got != tt.docker {
				t.Errorf("DockerGPUArgs = %q, want %q", got, tt.docker)
			}
		})
	}
}

// argsRunner records the argv of the one command it runs.
type argsRunner struct{ args []string }

func (r *argsRunner) Run(_ context.Context, _ string, args ...string) (string, string, int, error) {
	r.args = args
	return "", "", 0, nil
}

func followingValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestDockerRuntime_PerTaskGPU(t *testing.T) {
	tests := []struct {
		name string
		gpu  GPUConfig
		want string // --gpus values joined by "|"
		cuda bool   // -e CUDA_VISIBLE_DEVICES present
	}{
		{"assigned", GPUConfig{Enabled: true, Devices: []string{"GPU-a", "GPU-b"}}, `"device=GPU-a,GPU-b"`, false},
		{"denied", GPUConfig{AccessDenied: true}, "", false},
		{"legacy list keeps its env", GPUConfig{Enabled: true, DeviceID: "0"}, `"device=0"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &argsRunner{}
			if _, err := NewDockerWithRunner(r).Run(context.Background(), RunSpec{
				Image: "img", Command: []string{"true"}, WorkDir: t.TempDir(), GPU: tt.gpu,
			}); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(followingValues(r.args, "--gpus"), "|"); got != tt.want {
				t.Errorf("--gpus = %q, want %q (args %q)", got, tt.want, r.args)
			}
			cuda := false
			for _, v := range followingValues(r.args, "-e") {
				cuda = cuda || strings.HasPrefix(v, "CUDA_VISIBLE_DEVICES=")
			}
			if cuda != tt.cuda {
				t.Errorf("-e CUDA_VISIBLE_DEVICES present = %v, want %v", cuda, tt.cuda)
			}
		})
	}
}

func TestApptainerRuntime_PerTaskGPU(t *testing.T) {
	tests := []struct {
		name string
		gpu  GPUConfig
		nv   bool
		env  string // CUDA_* --env values joined by "|"
	}{
		{"assigned", GPUConfig{Enabled: true, Devices: []string{"GPU-a"}}, true, "CUDA_VISIBLE_DEVICES=GPU-a|CUDA_DEVICE_ORDER=PCI_BUS_ID"},
		{"denied", GPUConfig{AccessDenied: true}, false, "CUDA_VISIBLE_DEVICES="},
		{"legacy all", GPUConfig{Enabled: true}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &argsRunner{}
			if _, err := NewApptainerWithRunner(r).Run(context.Background(), RunSpec{
				Image: "img", Command: []string{"true"}, WorkDir: t.TempDir(), GPU: tt.gpu,
			}); err != nil {
				t.Fatal(err)
			}
			nv := false
			for _, a := range r.args {
				nv = nv || a == "--nv"
			}
			if nv != tt.nv {
				t.Errorf("--nv present = %v, want %v (args %q)", nv, tt.nv, r.args)
			}
			var cuda []string
			for _, v := range followingValues(r.args, "--env") {
				if strings.HasPrefix(v, "CUDA_") {
					cuda = append(cuda, v)
				}
			}
			if got := strings.Join(cuda, "|"); got != tt.env {
				t.Errorf("CUDA --env = %q, want %q", got, tt.env)
			}
		})
	}
}

func TestBareRuntime_PerTaskGPUEnv(t *testing.T) {
	t.Setenv("CUDA_VISIBLE_DEVICES", "7")
	tests := []struct {
		name string
		gpu  GPUConfig
		want string // the CUDA_VISIBLE_DEVICES line(s) printed by env
	}{
		{"assigned", GPUConfig{Enabled: true, Devices: []string{"GPU-a", "GPU-b"}}, "CUDA_VISIBLE_DEVICES=GPU-a,GPU-b"},
		{"denied", GPUConfig{AccessDenied: true}, "CUDA_VISIBLE_DEVICES="},
		{"legacy all keeps the inherited value", GPUConfig{Enabled: true}, "CUDA_VISIBLE_DEVICES=7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := NewBare().Run(context.Background(), RunSpec{Command: []string{"env"}, WorkDir: t.TempDir(), GPU: tt.gpu})
			if err != nil {
				t.Skipf("env not runnable here: %v", err)
			}
			var got []string
			for _, l := range strings.Split(res.Stdout, "\n") {
				if strings.HasPrefix(l, "CUDA_VISIBLE_DEVICES=") {
					got = append(got, l)
				}
			}
			if strings.Join(got, "|") != tt.want {
				t.Errorf("env CUDA_VISIBLE_DEVICES = %q, want %q", got, tt.want)
			}
		})
	}
}
