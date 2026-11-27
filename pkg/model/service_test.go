package model

import (
	"strings"
	"testing"
)

// validService returns a spec with every default applied.
func validService() *ServiceSpec {
	s := &ServiceSpec{PortInput: "port"}
	s.ApplyDefaults()
	return s
}

func TestServiceSpec_ApplyDefaults(t *testing.T) {
	s := &ServiceSpec{PortInput: "port"}
	s.ApplyDefaults()
	want := ServiceSpec{
		PortInput:        "port",
		Scheme:           "http",
		StopGraceSeconds: 30,
		Health: ServiceHealth{
			Path:                "/health",
			StartTimeoutSeconds: 600,
			IntervalSeconds:     10,
			FailureThreshold:    3,
		},
	}
	if *s != want {
		t.Errorf("defaults = %+v, want %+v", *s, want)
	}

	// Set fields are kept.
	s = &ServiceSpec{PortInput: "p", Scheme: "https", StopGraceSeconds: 5,
		Health: ServiceHealth{Path: "/ok", StartTimeoutSeconds: 7, IntervalSeconds: 8, FailureThreshold: 9}}
	before := *s
	s.ApplyDefaults()
	if *s != before {
		t.Errorf("ApplyDefaults changed set fields: %+v, want %+v", *s, before)
	}

	var nilSpec *ServiceSpec
	nilSpec.ApplyDefaults() // must not panic
}

func TestServiceSpec_Validate(t *testing.T) {
	long := strings.Repeat("a", 257)
	tests := []struct {
		name    string
		mutate  func(s *ServiceSpec)
		wantErr string // "" = valid
	}{
		{"defaults", func(s *ServiceSpec) {}, ""},
		{"https", func(s *ServiceSpec) { s.Scheme = "https" }, ""},
		{"empty scheme means http", func(s *ServiceSpec) { s.Scheme = "" }, ""},
		{"ready path without contains", func(s *ServiceSpec) { s.Health.ReadyPath = "/v1/models" }, ""},
		{"ready path and contains", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/v1/models"
			s.Health.ReadyContains = "my model"
		}, ""},
		{"bounds low", func(s *ServiceSpec) {
			s.StopGraceSeconds, s.Health.StartTimeoutSeconds, s.Health.IntervalSeconds, s.Health.FailureThreshold = 1, 1, 1, 1
		}, ""},
		{"bounds high", func(s *ServiceSpec) {
			s.StopGraceSeconds, s.Health.StartTimeoutSeconds, s.Health.IntervalSeconds, s.Health.FailureThreshold = 600, 3600, 300, 100
		}, ""},
		{"checksum", func(s *ServiceSpec) { s.WeightsChecksum = "sha256:" + strings.Repeat("0", 64) }, ""},
		{"path of 256", func(s *ServiceSpec) { s.Health.Path = "/" + strings.Repeat("a", 255) }, ""},
		{"path with query and symbols", func(s *ServiceSpec) { s.Health.Path = "/v1/health?full=1&x=~!$" }, ""},
		{"ready contains with spaces and non-ASCII", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = "naïve model ✓"
		}, ""},
		{"ready contains of 256 bytes", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = strings.Repeat("é", 128) // 2 bytes each
		}, ""},
		{"checksum with symbols", func(s *ServiceSpec) { s.WeightsChecksum = "sha256:AbC+/=_-" }, ""},

		{"no port input", func(s *ServiceSpec) { s.PortInput = "" }, "port_input is required"},
		{"blank port input", func(s *ServiceSpec) { s.PortInput = "  " }, "port_input is required"},
		{"bad scheme", func(s *ServiceSpec) { s.Scheme = "grpc" }, "scheme must be http or https"},
		{"stop grace 0", func(s *ServiceSpec) { s.StopGraceSeconds = 0 }, "stop_grace must be between 1 and 600"},
		{"stop grace 601", func(s *ServiceSpec) { s.StopGraceSeconds = 601 }, "stop_grace must be between 1 and 600"},
		{"start timeout 3601", func(s *ServiceSpec) { s.Health.StartTimeoutSeconds = 3601 }, "health.start_timeout must be between 1 and 3600"},
		{"start timeout negative", func(s *ServiceSpec) { s.Health.StartTimeoutSeconds = -1 }, "health.start_timeout must be between 1 and 3600"},
		{"interval 301", func(s *ServiceSpec) { s.Health.IntervalSeconds = 301 }, "health.interval must be between 1 and 300"},
		{"failure threshold 101", func(s *ServiceSpec) { s.Health.FailureThreshold = 101 }, "health.failure_threshold must be between 1 and 100"},
		{"empty path", func(s *ServiceSpec) { s.Health.Path = "" }, "health.path must not be empty"},
		{"relative path", func(s *ServiceSpec) { s.Health.Path = "health" }, `health.path must start with "/"`},
		{"path with space", func(s *ServiceSpec) { s.Health.Path = "/he alth" }, "health.path must contain only printable ASCII"},
		{"path with control", func(s *ServiceSpec) { s.Health.Path = "/he\x00alth" }, "health.path must contain only printable ASCII"},
		{"path with newline", func(s *ServiceSpec) { s.Health.Path = "/health\n" }, "health.path must contain only printable ASCII"},
		{"path with DEL", func(s *ServiceSpec) { s.Health.Path = "/health\x7f" }, "health.path must contain only printable ASCII"},
		{"path with non-ASCII", func(s *ServiceSpec) { s.Health.Path = "/gesundheit/é" }, "health.path must contain only printable ASCII"},
		{"path starting with //", func(s *ServiceSpec) { s.Health.Path = "//evil.example/health" }, `health.path must not start with "//"`},
		{"path with fragment", func(s *ServiceSpec) { s.Health.Path = "/health#x" }, `health.path must not contain "#"`},
		{"path too long", func(s *ServiceSpec) { s.Health.Path = "/" + long }, "health.path is longer than 256 bytes"},
		{"relative ready path", func(s *ServiceSpec) { s.Health.ReadyPath = "v1/models" }, `health.ready_path must start with "/"`},
		{"ready path with tab", func(s *ServiceSpec) { s.Health.ReadyPath = "/v1\tmodels" }, "health.ready_path must contain only printable ASCII"},
		{"ready path with non-ASCII", func(s *ServiceSpec) { s.Health.ReadyPath = "/v1/modèles" }, "health.ready_path must contain only printable ASCII"},
		{"ready path starting with //", func(s *ServiceSpec) { s.Health.ReadyPath = "//v1/models" }, `health.ready_path must not start with "//"`},
		{"ready path with fragment", func(s *ServiceSpec) { s.Health.ReadyPath = "/v1/models#top" }, `health.ready_path must not contain "#"`},
		{"contains without ready path", func(s *ServiceSpec) { s.Health.ReadyContains = "x" }, "health.ready_contains requires health.ready_path"},
		{"contains too long", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = long
		}, "health.ready_contains is longer than 256 bytes"},
		{"contains of 258 bytes in 129 characters", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = strings.Repeat("é", 129)
		}, "health.ready_contains is longer than 256 bytes"},
		{"contains with newline", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = "ready\n"
		}, "health.ready_contains must not contain control characters"},
		{"contains with C1 control", func(s *ServiceSpec) {
			s.Health.ReadyPath = "/r"
			s.Health.ReadyContains = "ready\u0085"
		}, "health.ready_contains must not contain control characters"},
		{"checksum too long", func(s *ServiceSpec) { s.WeightsChecksum = long }, "weights_checksum is longer than 256 bytes"},
		{"checksum with space", func(s *ServiceSpec) { s.WeightsChecksum = "sha256: abc" }, "weights_checksum must contain only printable ASCII"},
		{"checksum with non-ASCII", func(s *ServiceSpec) { s.WeightsChecksum = "sha256:ä" }, "weights_checksum must contain only printable ASCII"},
		{"checksum with control", func(s *ServiceSpec) { s.WeightsChecksum = "sha256:\x1b" }, "weights_checksum must contain only printable ASCII"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validService()
			tt.mutate(s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}

	var nilSpec *ServiceSpec
	if nilSpec.Validate() == nil {
		t.Error("a nil spec must not validate")
	}
}

func TestCheckServicePortInput(t *testing.T) {
	tool := &Tool{ID: "srv", Inputs: []ToolInput{{ID: "model", Type: "string"}, {ID: "port", Type: "int"}, {ID: "opt", Type: "int?"}}}
	tests := []struct {
		name    string
		tool    *Tool
		port    string
		wantErr string
	}{
		{"int input", tool, "port", ""},
		{"missing input", tool, "nope", `port_input "nope" is not an input of tool "srv"`},
		{"string input", tool, "model", `must name an input of type int, it is "string"`},
		{"optional int", tool, "opt", `it is "int?"`},
		{"no tool", nil, "port", "must be a CommandLineTool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckServicePortInput(tt.tool, &ServiceSpec{PortInput: tt.port})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

// serviceWorkflow returns a valid one-step service workflow.
func serviceWorkflow() *Workflow {
	return &Workflow{
		ID: "wf_1",
		Steps: []Step{{
			ID: "run_tool",
			ToolInline: &Tool{ID: "srv", Class: "CommandLineTool",
				Inputs: []ToolInput{{ID: "port", Type: "int"}}},
			Hints: &StepHints{DockerImage: "vllm.sif", WorkerGroup: "gpu", Service: validService()},
		}},
	}
}

func TestValidateServiceWorkflow(t *testing.T) {
	tests := []struct {
		name      string
		wf        func() *Workflow
		wantSvc   bool
		wantGroup string
		wantErr   string
	}{
		{"nil workflow", func() *Workflow { return nil }, false, "", ""},
		{"no hints", func() *Workflow { return &Workflow{Steps: []Step{{ID: "a"}, {ID: "b", Scatter: []string{"x"}}}} }, false, "", ""},
		{"ordinary hints", func() *Workflow {
			return &Workflow{Steps: []Step{{ID: "a", Hints: &StepHints{WorkerGroup: "default"}}}}
		}, false, "", ""},
		{"valid", serviceWorkflow, true, "gpu", ""},
		{"two steps", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps = append(wf.Steps, Step{ID: "other"})
			return wf
		}, true, "gpu", "exactly one step, it has 2"},
		{"service on second step", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps = append([]Step{{ID: "first"}}, wf.Steps...)
			return wf
		}, true, "gpu", "exactly one step"},
		{"scatter", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].Scatter = []string{"port"}
			return wf
		}, true, "gpu", "must not scatter"},
		{"no container", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].Hints.DockerImage = ""
			return wf
		}, true, "gpu", "must run in a container"},
		{"no group", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].Hints.WorkerGroup = ""
			return wf
		}, true, "", "worker group other than"},
		{"default group", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].Hints.WorkerGroup = "default"
			return wf
		}, true, "default", "worker group other than"},
		{"port not int", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].ToolInline.Inputs[0].Type = "string"
			return wf
		}, true, "gpu", "of type int"},
		{"no tool", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].ToolInline = nil
			return wf
		}, true, "gpu", "must be a CommandLineTool"},
		{"service only on the tool hints", func() *Workflow {
			wf := serviceWorkflow()
			wf.Steps[0].ToolInline.Hints = &StepHints{Service: validService()}
			wf.Steps[0].Hints.Service = nil
			return wf
		}, true, "gpu", "no resolved gowe:Service hint"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group, isSvc, err := ValidateServiceWorkflow(tt.wf())
			if isSvc != tt.wantSvc || group != tt.wantGroup {
				t.Errorf("(group, isService) = (%q, %v), want (%q, %v)", group, isSvc, tt.wantGroup, tt.wantSvc)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
