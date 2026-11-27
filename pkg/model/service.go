package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Defaults and bounds of a ServiceSpec (gowe:Service hint).
const (
	DefaultServiceScheme              = "http"
	DefaultServiceHealthPath          = "/health"
	DefaultServiceStartTimeoutSeconds = 600
	DefaultServiceIntervalSeconds     = 10
	DefaultServiceFailureThreshold    = 3
	DefaultServiceStopGraceSeconds    = 30

	maxServiceStartTimeoutSeconds = 3600
	maxServiceIntervalSeconds     = 300
	maxServiceFailureThreshold    = 100
	maxServiceStopGraceSeconds    = 600
	maxServiceStringLen           = 256
)

// ServiceSpec marks a tool as a long-running service (gowe:Service hint).
// Experimental: a task carrying it (Task.IsService) waits for a service-mode
// worker of its group and is never retried, requeued or re-run by the server;
// workers do not run services yet.
type ServiceSpec struct {
	PortInput        string        `json:"port_input"`                 // id of the tool's int input the worker sets to the chosen port
	Scheme           string        `json:"scheme,omitempty"`           // "http" (default) or "https"
	Health           ServiceHealth `json:"health"`                     // readiness and liveness checks
	StopGraceSeconds int           `json:"stop_grace_seconds"`         // default 30, 1..600
	WeightsChecksum  string        `json:"weights_checksum,omitempty"` // recorded only
}

// ServiceHealth describes how a service's health is checked.
type ServiceHealth struct {
	Path                string `json:"path"`                     // default "/health"
	ReadyPath           string `json:"ready_path,omitempty"`     // optional second GET
	ReadyContains       string `json:"ready_contains,omitempty"` // its body must contain this; requires ReadyPath
	StartTimeoutSeconds int    `json:"start_timeout_seconds"`    // default 600, 1..3600
	IntervalSeconds     int    `json:"interval_seconds"`         // default 10, 1..300
	FailureThreshold    int    `json:"failure_threshold"`        // default 3, 1..100
}

// ApplyDefaults fills every unset (zero) field with its default. A nil
// receiver is left alone.
func (s *ServiceSpec) ApplyDefaults() {
	if s == nil {
		return
	}
	if s.Scheme == "" {
		s.Scheme = DefaultServiceScheme
	}
	if s.StopGraceSeconds == 0 {
		s.StopGraceSeconds = DefaultServiceStopGraceSeconds
	}
	h := &s.Health
	if h.Path == "" {
		h.Path = DefaultServiceHealthPath
	}
	if h.StartTimeoutSeconds == 0 {
		h.StartTimeoutSeconds = DefaultServiceStartTimeoutSeconds
	}
	if h.IntervalSeconds == 0 {
		h.IntervalSeconds = DefaultServiceIntervalSeconds
	}
	if h.FailureThreshold == 0 {
		h.FailureThreshold = DefaultServiceFailureThreshold
	}
}

// Validate checks a ServiceSpec after ApplyDefaults. Messages name the hint's
// keys (port_input, health.start_timeout, ...), the only way a spec is made.
func (s *ServiceSpec) Validate() error {
	if s == nil {
		return errors.New("no service spec")
	}
	if strings.TrimSpace(s.PortInput) == "" {
		return errors.New("port_input is required (the id of the tool's int input that receives the port)")
	}
	switch s.Scheme {
	case "", "http", "https":
	default:
		return fmt.Errorf("scheme must be http or https, got %q", s.Scheme)
	}
	if err := checkIntRange("stop_grace", s.StopGraceSeconds, 1, maxServiceStopGraceSeconds); err != nil {
		return err
	}
	if err := checkPrintableASCII("weights_checksum", s.WeightsChecksum); err != nil {
		return err
	}
	h := s.Health
	if h.Path == "" {
		return errors.New("health.path must not be empty")
	}
	if err := checkHTTPPath("health.path", h.Path); err != nil {
		return err
	}
	if h.ReadyPath != "" {
		if err := checkHTTPPath("health.ready_path", h.ReadyPath); err != nil {
			return err
		}
	}
	if len(h.ReadyContains) > maxServiceStringLen {
		return fmt.Errorf("health.ready_contains is longer than %d bytes", maxServiceStringLen)
	}
	for _, r := range h.ReadyContains {
		if unicode.IsControl(r) {
			return errors.New("health.ready_contains must not contain control characters")
		}
	}
	if h.ReadyContains != "" && h.ReadyPath == "" {
		return errors.New("health.ready_contains requires health.ready_path")
	}
	if err := checkIntRange("health.start_timeout", h.StartTimeoutSeconds, 1, maxServiceStartTimeoutSeconds); err != nil {
		return err
	}
	if err := checkIntRange("health.interval", h.IntervalSeconds, 1, maxServiceIntervalSeconds); err != nil {
		return err
	}
	return checkIntRange("health.failure_threshold", h.FailureThreshold, 1, maxServiceFailureThreshold)
}

// checkIntRange refuses v outside lo..hi.
func checkIntRange(field string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be between %d and %d, got %d", field, lo, hi, v)
	}
	return nil
}

// checkPrintableASCII refuses a value longer than maxServiceStringLen bytes
// or holding any byte outside printable ASCII without the space (0x21..0x7e).
// The empty value is accepted.
func checkPrintableASCII(field, v string) error {
	if len(v) > maxServiceStringLen {
		return fmt.Errorf("%s is longer than %d bytes", field, maxServiceStringLen)
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x21 || c > 0x7e {
			return fmt.Errorf("%s must contain only printable ASCII characters (0x21-0x7e: no spaces, control or non-ASCII characters)", field)
		}
	}
	return nil
}

// checkHTTPPath refuses a path that does not start with "/", starts with
// "//" (it would be read as a host), fails checkPrintableASCII, or holds a
// "#" (a fragment is never sent).
func checkHTTPPath(field, v string) error {
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("%s must start with \"/\", got %q", field, v)
	}
	if strings.HasPrefix(v, "//") {
		return fmt.Errorf("%s must not start with \"//\", got %q", field, v)
	}
	if err := checkPrintableASCII(field, v); err != nil {
		return err
	}
	if strings.Contains(v, "#") {
		return fmt.Errorf("%s must not contain \"#\", got %q", field, v)
	}
	return nil
}

// CheckServicePortInput reports whether spec.PortInput names an input of
// tool whose type is int. tool is nil when the step does not run a
// CommandLineTool of the same document (an ExpressionTool, a sub-workflow,
// an unresolved reference): a service must be a CommandLineTool.
func CheckServicePortInput(tool *Tool, spec *ServiceSpec) error {
	if spec == nil {
		return nil
	}
	if tool == nil {
		return errors.New("a service must be a CommandLineTool of this document (not an ExpressionTool or a sub-workflow)")
	}
	for _, in := range tool.Inputs {
		if in.ID != spec.PortInput {
			continue
		}
		if in.Type != "int" {
			return fmt.Errorf("port_input %q must name an input of type int, it is %q", spec.PortInput, in.Type)
		}
		return nil
	}
	return fmt.Errorf("port_input %q is not an input of tool %q", spec.PortInput, tool.ID)
}

// defaultWorkerGroupName is the group of workers started without --group.
const defaultWorkerGroupName = "default"

// ValidateServiceWorkflow applies the structural registration rule for a
// workflow that contains a service tool (a step whose hints carry Service).
// isService is false, and nothing else is checked, for any other workflow.
// For a service workflow it requires: exactly one step, without scatter; a
// container image (resolved DockerImage); no executor hint other than worker
// or container (a service runs only on the worker executor); a worker group other than the
// default one; port_input naming an int input of the step's tool. group is
// the service step's worker group, also returned (best effort) with an error
// so a caller can still tell whose workflow it is. Whether the caller may
// register it for that group is the caller's check.
func ValidateServiceWorkflow(wf *Workflow) (group string, isService bool, err error) {
	if wf == nil {
		return "", false, nil
	}
	svcIdx := -1
	for i := range wf.Steps {
		st := &wf.Steps[i]
		if (st.Hints != nil && st.Hints.Service != nil) ||
			(st.ToolInline != nil && st.ToolInline.Hints != nil && st.ToolInline.Hints.Service != nil) {
			svcIdx = i
			break
		}
	}
	if svcIdx < 0 {
		return "", false, nil
	}
	st := &wf.Steps[svcIdx]
	if st.Hints != nil {
		group = st.Hints.WorkerGroup
	}
	if len(wf.Steps) != 1 {
		return group, true, fmt.Errorf("a service workflow must have exactly one step, it has %d", len(wf.Steps))
	}
	if len(st.Scatter) > 0 {
		return group, true, fmt.Errorf("the service step %q must not scatter", st.ID)
	}
	if st.Hints == nil || st.Hints.Service == nil {
		// The merged step hints always carry the tool's Service; reaching
		// here means the workflow was not built by the parser.
		return group, true, fmt.Errorf("the service step %q has no resolved gowe:Service hint", st.ID)
	}
	if st.Hints.DockerImage == "" {
		return group, true, fmt.Errorf("the service step %q must run in a container (DockerRequirement.dockerPull or gowe:Execution.docker_image)", st.ID)
	}
	switch st.Hints.ExecutorType {
	case "", ExecutorTypeWorker, ExecutorTypeContainer:
	default:
		return group, true, fmt.Errorf("the service step %q runs only on the worker executor: gowe:Execution.executor %q is not allowed (use worker, or leave it unset)", st.ID, st.Hints.ExecutorType)
	}
	if group == "" || group == defaultWorkerGroupName {
		return group, true, fmt.Errorf("the service step %q must name a worker group other than %q (gowe:Execution.worker_group)", st.ID, defaultWorkerGroupName)
	}
	return group, true, CheckServicePortInput(st.ToolInline, st.Hints.Service)
}
