package toolexec

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// The GPU cases every runtime must handle.
var gpuCases = []struct {
	name string
	gpu  GPUConfig
}{
	{"legacy all GPUs", GPUConfig{Enabled: true}},
	{"legacy id list", GPUConfig{Enabled: true, DeviceID: "0,1"}},
	{"assigned devices", GPUConfig{Enabled: true, Devices: []string{"GPU-a", "GPU-b"}}},
	{"assigned single device", GPUConfig{Enabled: true, Devices: []string{"GPU-a"}}},
	{"denied", GPUConfig{AccessDenied: true}},
	{"no GPU", GPUConfig{}},
}

// runFakeContainerCLI puts a stub named bin ("docker" or "apptainer") first on
// PATH, runs the tool in mode with gpu, and returns the stub's argv (one
// element per entry, exactly as exec'd). No container runtime or GPU is used.
func runFakeContainerCLI(t *testing.T, bin string, mode ExecutionMode, gpu GPUConfig) []string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argsFile + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	_, _ = e.Execute(context.Background(), &Options{
		Tool:        &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"},
		Command:     &cmdline.BuildResult{Command: []string{"echo", "hi"}},
		Inputs:      map[string]any{},
		WorkDir:     t.TempDir(),
		Mode:        mode,
		DockerImage: "alpine",
		GPU:         gpu,
	})
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake %s was not invoked: %v", bin, err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// envValues returns every "--env"/"-e" value of args.
func envValues(args []string, flag string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			out = append(out, args[i+1])
		}
	}
	return out
}

func hasPrefixIn(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

func TestExecuteInDocker_GPUArgs(t *testing.T) {
	want := map[string][]string{ // the --gpus argv element(s); nil = no --gpus
		"legacy all GPUs":        {"all"},
		"legacy id list":         {`"device=0,1"`},
		"assigned devices":       {`"device=GPU-a,GPU-b"`},
		"assigned single device": {`"device=GPU-a"`},
		"denied":                 nil,
		"no GPU":                 nil,
	}
	for _, tc := range gpuCases {
		t.Run(tc.name, func(t *testing.T) {
			args := runFakeContainerCLI(t, "docker", ModeDocker, tc.gpu)
			got := envValues(args, "--gpus")
			if strings.Join(got, "|") != strings.Join(want[tc.name], "|") {
				t.Errorf("--gpus values = %q, want %q (args %q)", got, want[tc.name], args)
			}
			// Docker renumbers the devices it passes in: never CUDA_VISIBLE_DEVICES.
			for _, a := range args {
				if strings.Contains(a, "CUDA_VISIBLE_DEVICES") || strings.Contains(a, "CUDA_DEVICE_ORDER") {
					t.Errorf("docker argv carries %q", a)
				}
			}
		})
	}
}

func TestExecuteInApptainer_GPUArgs(t *testing.T) {
	tests := map[string]struct {
		nv  bool
		env []string // CUDA_* --env values, in order
	}{
		"legacy all GPUs":        {true, nil},
		"legacy id list":         {true, []string{"CUDA_VISIBLE_DEVICES=0,1", "CUDA_DEVICE_ORDER=PCI_BUS_ID"}},
		"assigned devices":       {true, []string{"CUDA_VISIBLE_DEVICES=GPU-a,GPU-b", "CUDA_DEVICE_ORDER=PCI_BUS_ID"}},
		"assigned single device": {true, []string{"CUDA_VISIBLE_DEVICES=GPU-a", "CUDA_DEVICE_ORDER=PCI_BUS_ID"}},
		"denied":                 {false, []string{"CUDA_VISIBLE_DEVICES="}},
		"no GPU":                 {false, nil},
	}
	for _, tc := range gpuCases {
		t.Run(tc.name, func(t *testing.T) {
			want := tests[tc.name]
			args := runFakeContainerCLI(t, "apptainer", ModeApptainer, tc.gpu)
			if got := indexOf(args, "--nv") >= 0; got != want.nv {
				t.Errorf("--nv present = %v, want %v (args %q)", got, want.nv, args)
			}
			var cuda []string
			for _, v := range envValues(args, "--env") {
				if strings.HasPrefix(v, "CUDA_") {
					cuda = append(cuda, v)
				}
			}
			if strings.Join(cuda, "|") != strings.Join(want.env, "|") {
				t.Errorf("CUDA --env values = %q, want %q", cuda, want.env)
			}
		})
	}
}

func TestExecuteLocal_GPUEnv(t *testing.T) {
	tests := map[string]struct {
		visible  string // the one CUDA_VISIBLE_DEVICES value the tool sees (unless absent)
		absent   bool   // not set by the worker (an inherited value passes only with GPU access)
		order    bool   // CUDA_DEVICE_ORDER=PCI_BUS_ID present
		hostCUDA bool   // host CUDA_HOME passes the allow-list
	}{
		"legacy all GPUs":        {absent: true, hostCUDA: true},
		"legacy id list":         {visible: "0,1", order: true, hostCUDA: true},
		"assigned devices":       {visible: "GPU-a,GPU-b", order: true, hostCUDA: true},
		"assigned single device": {visible: "GPU-a", order: true, hostCUDA: true},
		"denied":                 {visible: "", order: false, hostCUDA: false},
		"no GPU":                 {absent: true, hostCUDA: false},
	}
	for _, tc := range gpuCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CUDA_HOME", "/usr/local/cuda")
			t.Setenv("CUDA_VISIBLE_DEVICES", "7") // a value inherited by the worker
			t.Setenv("CUDA_DEVICE_ORDER", "FASTEST_FIRST")
			want := tests[tc.name]
			out := localEnvDump(t, func(o *Options) { o.GPU = tc.gpu })
			lines := strings.Split(out, "\n")
			var visible []string
			for _, l := range lines {
				if strings.HasPrefix(l, "CUDA_VISIBLE_DEVICES=") {
					visible = append(visible, strings.TrimPrefix(l, "CUDA_VISIBLE_DEVICES="))
				}
			}
			switch {
			case want.absent && want.hostCUDA:
				// Legacy every GPU: the inherited value passes the allow-list unchanged.
				if strings.Join(visible, "|") != "7" {
					t.Errorf("CUDA_VISIBLE_DEVICES = %q, want the inherited 7", visible)
				}
			case want.absent:
				if len(visible) != 0 {
					t.Errorf("CUDA_VISIBLE_DEVICES = %q, want none", visible)
				}
			default:
				if len(visible) != 1 || visible[0] != want.visible {
					t.Errorf("CUDA_VISIBLE_DEVICES = %q, want exactly %q", visible, want.visible)
				}
			}
			if got := hasPrefixIn(lines, "CUDA_DEVICE_ORDER=PCI_BUS_ID"); got != want.order {
				t.Errorf("CUDA_DEVICE_ORDER=PCI_BUS_ID present = %v, want %v", got, want.order)
			}
			if want.order && hasPrefixIn(lines, "CUDA_DEVICE_ORDER=FASTEST_FIRST") {
				t.Error("inherited CUDA_DEVICE_ORDER not overridden")
			}
			if got := hasPrefixIn(lines, "CUDA_HOME="); got != want.hostCUDA {
				t.Errorf("host CUDA_HOME present = %v, want %v", got, want.hostCUDA)
			}
		})
	}
}
