//go:build unix

package toolexec

// Golden argv of the batch Apptainer launch. This file uses only symbols that
// existed before worker service mode (Options without Service, the stub of
// installFakeApptainer / runFakeApptainer), so it can be run unchanged on
// main to prove that the batch argv did not change.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/pkg/cwl"
)

// bgRoot is the placeholder for the test's temporary root in the goldens.
const bgRoot = "$ROOT"

// bgStable replaces the temporary root (symlink-resolved and as given) by
// bgRoot in every argument.
func bgStable(args []string, root string) []string {
	resolved := ResolveSymlinks(root)
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, resolved, bgRoot)
		a = strings.ReplaceAll(a, root, bgRoot)
		out[i] = a
	}
	return out
}

// bgArgv is the golden argv for WorkDir $ROOT/task: the fixed head, binds
// (after the /tmp mount), env (after TMPDIR=/tmp: environment, GPU and
// resource flags), the network flags unless hostNet, the image and the
// command.
func bgArgv(binds, env []string, hostNet bool, image string, command ...string) []string {
	args := []string{
		"exec", "--containall", "--cleanenv",
		"--home", bgRoot + "/task:/var/spool/cwl",
		"--pwd", "/var/spool/cwl",
		"--mount", "type=bind,source=" + bgRoot + "/task_tmp,destination=/tmp",
	}
	args = append(args, binds...)
	args = append(args, "--env", "TMPDIR=/tmp")
	args = append(args, env...)
	if !hostNet {
		args = append(args, "--net", "--network", "none")
	}
	args = append(args, image)
	return append(args, command...)
}

func TestApptainerArgs_BatchGoldenFull(t *testing.T) {
	const secretValue = "tok-value-0123456789abcdef"
	tests := []struct {
		name    string
		mutate  func(o *Options, root string)
		want    []string
		wantEnv string // a line the stub's APPTAINERENV_* record must contain
	}{
		{
			name: "defaults",
			want: bgArgv(nil, nil, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "extra binds",
			mutate: func(o *Options, root string) {
				// Created so that symlink resolution sees real directories;
				// a failure shows up as a changed argv.
				for _, d := range []string{"data", "refs"} {
					_ = os.MkdirAll(filepath.Join(root, d), 0o755)
				}
				o.ExtraBinds = []ExtraBind{
					{HostPath: filepath.Join(root, "data"), ContainerPath: "/data"},
					{HostPath: filepath.Join(root, "refs"), ContainerPath: "/local_databases/refs"},
				}
			},
			want: bgArgv([]string{
				"--mount", "type=bind,source=" + bgRoot + "/data,destination=/data",
				"--mount", "type=bind,source=" + bgRoot + "/refs,destination=/local_databases/refs",
			}, nil, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "NetworkAccess true",
			mutate: func(o *Options, _ string) {
				o.Tool.Requirements = map[string]any{"NetworkAccess": map[string]any{"networkAccess": true}}
			},
			want: bgArgv(nil, nil, true, "docker://alpine", "echo", "hi"),
		},
		{
			name: "NetworkAccess false",
			mutate: func(o *Options, _ string) {
				o.Tool.Requirements = map[string]any{"NetworkAccess": map[string]any{"networkAccess": false}}
			},
			want: bgArgv(nil, nil, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "ShellCommandRequirement",
			mutate: func(o *Options, _ string) {
				o.Tool.Requirements = map[string]any{"ShellCommandRequirement": map[string]any{}}
				o.Command = &cmdline.BuildResult{
					Command:    []string{"echo", "a b", "|", "wc", "-c"},
					ShellQuote: []bool{true, true, false, true, true},
				}
			},
			want: bgArgv(nil, nil, false, "docker://alpine", "/bin/sh", "-c", "echo 'a b' | wc -c"),
		},
		{
			name: "GPU with static DeviceID",
			mutate: func(o *Options, _ string) {
				o.GPU = GPUConfig{Enabled: true, DeviceID: "0,1"}
			},
			want: bgArgv(nil, []string{
				"--nv", "--env", "CUDA_VISIBLE_DEVICES=0,1", "--env", "CUDA_DEVICE_ORDER=PCI_BUS_ID",
			}, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "GPU enabled, all devices",
			mutate: func(o *Options, _ string) {
				o.GPU = GPUConfig{Enabled: true}
			},
			want: bgArgv(nil, []string{"--nv"}, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "GPU access denied",
			mutate: func(o *Options, _ string) {
				o.GPU = GPUConfig{AccessDenied: true}
			},
			want: bgArgv(nil, []string{"--env", "CUDA_VISIBLE_DEVICES="}, false, "docker://alpine", "echo", "hi"),
		},
		{
			name: "sif image via ImageDir",
			mutate: func(o *Options, root string) {
				o.DockerImage = "model.sif"
				o.ImageDir = filepath.Join(root, "images")
			},
			want: bgArgv(nil, nil, false, bgRoot+"/images/model.sif", "echo", "hi"),
		},
		{
			name: "sif image via ResolvedImage",
			mutate: func(o *Options, root string) {
				o.DockerImage = "model.sif"
				o.ImageDir = filepath.Join(root, "images")
				o.ResolvedImage = filepath.Join(root, "resolved", "real-model.sif")
			},
			want: bgArgv(nil, nil, false, bgRoot+"/resolved/real-model.sif", "echo", "hi"),
		},
		{
			name: "secrets are not on the argv",
			mutate: func(o *Options, _ string) {
				o.SecretEnvVars = map[string]string{"API_TOKEN": secretValue}
			},
			want:    bgArgv(nil, nil, false, "docker://alpine", "echo", "hi"),
			wantEnv: "APPTAINERENV_API_TOKEN=" + secretValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			args, env := runFakeApptainer(t, func(o *Options) {
				o.WorkDir = filepath.Join(root, "task")
				o.Tool = &cwl.CommandLineTool{ID: "t", Class: "CommandLineTool"}
				if tt.mutate != nil {
					tt.mutate(o, root)
				}
			})
			if got := bgStable(args, root); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("argv changed:\n got %q\nwant %q", got, tt.want)
			}
			for _, a := range args {
				if strings.Contains(a, secretValue) || strings.Contains(a, "API_TOKEN") {
					t.Errorf("secret on the argv: %q", a)
				}
			}
			if tt.wantEnv != "" && !strings.Contains(env, tt.wantEnv+"\n") {
				t.Errorf("recorded APPTAINERENV_ environment lacks %q:\n%s", tt.wantEnv, env)
			}
		})
	}
}
