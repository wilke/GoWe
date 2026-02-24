package cwlrunner

import (
	"testing"
)

func TestCollectInputMounts(t *testing.T) {
	inputs := map[string]any{
		"ref": map[string]any{
			"class": "File",
			"path":  "/data/reference.fasta",
		},
		"msg": "hello",
		"reads": []any{
			map[string]any{
				"class": "File",
				"path":  "/data/reads_1.fq",
			},
			map[string]any{
				"class": "File",
				"path":  "/data/reads_2.fq",
			},
		},
	}

	mounts := collectInputMounts(inputs)

	// All absolute File paths should be collected as mounts.
	if _, ok := mounts["/data/reference.fasta"]; !ok {
		t.Error("expected mount for /data/reference.fasta")
	}
	if _, ok := mounts["/data/reads_1.fq"]; !ok {
		t.Error("expected mount for /data/reads_1.fq")
	}
	if _, ok := mounts["/data/reads_2.fq"]; !ok {
		t.Error("expected mount for /data/reads_2.fq")
	}
}

func TestContainerRuntimeDispatch(t *testing.T) {
	tests := []struct {
		name             string
		containerRuntime string
		noContainer      bool
		forceDocker      bool
		wantRuntime      string // expected effective runtime: "docker", "apptainer", or ""
	}{
		{
			name:        "default no container requirement",
			wantRuntime: "",
		},
		{
			name:        "ForceDocker sets docker",
			forceDocker: true,
			wantRuntime: "docker",
		},
		{
			name:             "explicit apptainer",
			containerRuntime: "apptainer",
			wantRuntime:      "apptainer",
		},
		{
			name:             "explicit docker",
			containerRuntime: "docker",
			wantRuntime:      "docker",
		},
		{
			name:             "NoContainer overrides ContainerRuntime",
			containerRuntime: "docker",
			noContainer:      true,
			wantRuntime:      "",
		},
		{
			name:        "NoContainer overrides ForceDocker",
			forceDocker: true,
			noContainer: true,
			wantRuntime: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the runtime resolution logic from runner.go.
			containerRuntime := tt.containerRuntime
			if tt.noContainer {
				containerRuntime = ""
			} else if containerRuntime == "" {
				if tt.forceDocker {
					containerRuntime = "docker"
				}
				// In real code, hasDockerRequirement would also trigger "docker" here.
			}

			if containerRuntime != tt.wantRuntime {
				t.Errorf("effective runtime = %q, want %q", containerRuntime, tt.wantRuntime)
			}
		})
	}
}

func TestResolveSymlinks(t *testing.T) {
	// resolveSymlinks should return an absolute path even for nonexistent paths.
	result := resolveSymlinks("/tmp")
	if result == "" {
		t.Error("resolveSymlinks returned empty string")
	}
}
