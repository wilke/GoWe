package model

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
// gowe-worker --service-mode runs it (internal/worker/service.go).
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

// States of a running service as its worker reports them
// (ServiceStatus.State).
const (
	ServiceStateStarting  = "starting"
	ServiceStateReady     = "ready"
	ServiceStateUnhealthy = "unhealthy"
	ServiceStateStopping  = "stopping"
)

// States the server shows in place of the reported one (Task.
// ApplyShownServiceState); a worker may not report them.
const (
	ServiceStateLost    = "lost"    // the task is RUNNING but its worker went silent (service_lost_at)
	ServiceStateStopped = "stopped" // the task is no longer RUNNING
)

// Bounds of a ServiceStatus a worker reports.
const (
	MaxServiceStatusGPUs = 64
	MaxServiceDetailLen  = 512
	// ServiceReadyAtSkew is how far in the future (the worker's clock ahead
	// of the server's) a reported ready_at may be.
	ServiceReadyAtSkew = 5 * time.Minute
	// ServiceStatusStaleAfter is how old a stored status (UpdatedAt) in a
	// state a worker reports may be before ServiceStatus.Line marks it with
	// the time it was last reported.
	ServiceStatusStaleAfter = 2 * time.Minute

	maxServiceHostLen    = 253
	maxServiceGPUUUIDLen = 96
	maxServiceGPUIndex   = 1023
	maxServiceDNSLabel   = 63
)

// ServiceStatus is what a worker reports about a running service.
type ServiceStatus struct {
	State       string       `json:"state"`                  // starting | ready | unhealthy | stopping
	Host        string       `json:"host,omitempty"`         // name clients use to reach it
	Port        int          `json:"port,omitempty"`         // 0: not reported
	Scheme      string       `json:"scheme,omitempty"`       // http | https
	GPUs        []ServiceGPU `json:"gpus,omitempty"`         // the devices the service was given
	ImageSHA256 string       `json:"image_sha256,omitempty"` // hex digest of the image file
	PID         int          `json:"pid,omitempty"`
	ReadyAt     *time.Time   `json:"ready_at,omitempty"` // first time it became ready
	Detail      string       `json:"detail,omitempty"`   // short human text, e.g. last health error
	UpdatedAt   time.Time    `json:"updated_at"`         // set by the SERVER when stored
}

// ServiceGPU is one GPU a service was given.
type ServiceGPU struct {
	UUID  string `json:"uuid,omitempty"`
	Index int    `json:"index"`
	// MemBudgetMB is reserved for a later per-GPU memory budget; keep the object shape.
	MemBudgetMB int64 `json:"mem_budget_mb,omitempty"`
}

// ServiceReport is one entry of a heartbeat's "services" list.
type ServiceReport struct {
	TaskID string `json:"task_id"`
	ServiceStatus
}

// Normalize makes a reported status storable instead of refusing it for a
// cosmetic fault: invalid UTF-8 in Detail becomes U+FFFD and every control
// character, format character (Unicode category Cf: bidirectional controls,
// zero-width characters, ...) and line or paragraph separator (Zl, Zp) in it
// a space. It cuts nothing off (Validate refuses a Detail longer than
// MaxServiceDetailLen bytes). A nil receiver is left alone.
func (s *ServiceStatus) Normalize() {
	if s == nil {
		return
	}
	s.Detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s.Detail, string(utf8.RuneError)))
}

// Validate checks a reported status after Normalize. now is the server's
// time: ready_at may not be later than now + ServiceReadyAtSkew. Messages
// never repeat a long reported value.
func (s *ServiceStatus) Validate(now time.Time) error {
	if s == nil {
		return errors.New("no service status")
	}
	switch s.State {
	case ServiceStateStarting, ServiceStateReady, ServiceStateUnhealthy, ServiceStateStopping:
	default:
		return fmt.Errorf("state must be starting, ready, unhealthy or stopping, got %s", shortQuote(s.State))
	}
	if err := checkServiceHost(s.Host); err != nil {
		return err
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535, got %d", s.Port)
	}
	switch s.Scheme {
	case "", "http", "https":
	default:
		return fmt.Errorf("scheme must be http or https, got %s", shortQuote(s.Scheme))
	}
	if len(s.GPUs) > MaxServiceStatusGPUs {
		return fmt.Errorf("at most %d gpus, got %d", MaxServiceStatusGPUs, len(s.GPUs))
	}
	for i, g := range s.GPUs {
		if len(g.UUID) > maxServiceGPUUUIDLen {
			return fmt.Errorf("gpus[%d].uuid is longer than %d bytes", i, maxServiceGPUUUIDLen)
		}
		for j := 0; j < len(g.UUID); j++ {
			if c := g.UUID[j]; c < 0x21 || c > 0x7e {
				return fmt.Errorf("gpus[%d].uuid must contain only printable ASCII characters (0x21-0x7e)", i)
			}
		}
		if g.Index < 0 || g.Index > maxServiceGPUIndex {
			return fmt.Errorf("gpus[%d].index must be between 0 and %d, got %d", i, maxServiceGPUIndex, g.Index)
		}
		if g.MemBudgetMB < 0 {
			return fmt.Errorf("gpus[%d].mem_budget_mb must not be negative, got %d", i, g.MemBudgetMB)
		}
	}
	if s.ImageSHA256 != "" && !isLowerHex(s.ImageSHA256, 64) {
		return errors.New("image_sha256 must be exactly 64 lower-case hex characters")
	}
	if s.PID < 0 {
		return fmt.Errorf("pid must not be negative, got %d", s.PID)
	}
	if len(s.Detail) > MaxServiceDetailLen {
		return fmt.Errorf("detail is longer than %d bytes", MaxServiceDetailLen)
	}
	if !utf8.ValidString(s.Detail) {
		return errors.New("detail is not valid UTF-8")
	}
	for _, r := range s.Detail {
		if unicode.IsControl(r) {
			return errors.New("detail must not contain control characters")
		}
	}
	if s.ReadyAt != nil {
		if s.ReadyAt.IsZero() {
			return errors.New("ready_at must not be the zero time")
		}
		if s.ReadyAt.After(now.Add(ServiceReadyAtSkew)) {
			return fmt.Errorf("ready_at must not be more than %s in the future", ServiceReadyAtSkew)
		}
	}
	return nil
}

// shortQuote quotes v for an error message, cut to 32 bytes.
func shortQuote(v string) string {
	if len(v) > 32 {
		return strconv.Quote(v[:32]) + "..."
	}
	return strconv.Quote(v)
}

// isLowerHex reports whether v is exactly n characters of 0-9a-f.
func isLowerHex(v string, n int) bool {
	if len(v) != n {
		return false
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidateServiceHost checks a host name or address with the rule the server
// applies to ServiceStatus.Host (checkServiceHost); "" is accepted. The
// worker checks its --advertise-host with it at start-up.
func ValidateServiceHost(h string) error {
	return checkServiceHost(h)
}

// checkServiceHost accepts "" or, in at most maxServiceHostLen bytes of
// printable ASCII without spaces: an IPv6 literal without brackets (the only
// form that may hold ":"), a dotted-decimal IPv4 address, or a DNS name
// (labels of 1-63 letters, digits and "-", neither first nor last, separated
// by single dots, no trailing dot, the last label not all digits). A name
// some resolvers read as a numeric IPv4 address is refused too: a last label
// of "0x" or "0X" followed only by hex digits, or a single label of hex
// digits only (such as 7f000001; a single label holding another letter, such
// as coconut, stays valid). Only the form is checked: nothing is looked up,
// and the worker may name any host.
func checkServiceHost(h string) error {
	if h == "" {
		return nil
	}
	if len(h) > maxServiceHostLen {
		return fmt.Errorf("host is longer than %d bytes", maxServiceHostLen)
	}
	for i := 0; i < len(h); i++ {
		if c := h[i]; c < 0x21 || c > 0x7e {
			return errors.New("host must contain only printable ASCII characters (0x21-0x7e: no spaces)")
		}
	}
	if strings.Contains(h, ":") {
		// net.ParseIP refuses brackets, zones ("%eth0") and anything else.
		if net.ParseIP(h) == nil {
			return errors.New("host contains \":\" but is not an IPv6 address (write it without brackets or port)")
		}
		return nil
	}
	if strings.Trim(h, "0123456789.") == "" {
		if ip := net.ParseIP(h); ip == nil || ip.To4() == nil {
			return errors.New("host is neither a dotted-decimal IPv4 address nor a DNS name")
		}
		return nil
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > maxServiceDNSLabel {
			return fmt.Errorf("host is not a valid DNS name (each label 1-%d characters, no empty label)", maxServiceDNSLabel)
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return errors.New("host is not a valid DNS name (a label must not start or end with \"-\")")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("host is not a valid DNS name (letters, digits, \"-\" and \".\" only)")
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.Trim(last, "0123456789") == "" {
		return errors.New("host is not a valid DNS name (the last label must not be all digits)")
	}
	if len(last) >= 2 && last[0] == '0' && (last[1] == 'x' || last[1] == 'X') && isHexDigits(last[2:]) {
		return errors.New("host is not a valid DNS name (the last label must not be a hexadecimal number such as 0x7f)")
	}
	if len(labels) == 1 && isHexDigits(last) {
		return errors.New("host is not a valid DNS name (a single label must not be all hexadecimal digits: it may be read as an IPv4 address)")
	}
	return nil
}

// isHexDigits reports whether every byte of v is 0-9, a-f or A-F (true for
// "").
func isHexDigits(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// Line renders the status on one line for people (web UI, gowe status):
// state, scheme://host:port, GPU indexes, ready since, and, when the state is
// one a worker reports (starting, ready, unhealthy, stopping; not lost or
// stopped) and UpdatedAt is more than ServiceStatusStaleAfter before now,
// " (last reported <UpdatedAt in RFC 3339 UTC>)". A nil receiver gives "".
func (s *ServiceStatus) Line(now time.Time) string {
	if s == nil {
		return ""
	}
	line := s.line()
	switch s.State {
	case ServiceStateStarting, ServiceStateReady, ServiceStateUnhealthy, ServiceStateStopping:
		if now.Sub(s.UpdatedAt) > ServiceStatusStaleAfter {
			line += " (last reported " + s.UpdatedAt.UTC().Format(time.RFC3339) + ")"
		}
	}
	return line
}

// line is Line without the staleness suffix.
func (s *ServiceStatus) line() string {
	parts := []string{s.State}
	if s.Host != "" {
		scheme := s.Scheme
		if scheme == "" {
			scheme = DefaultServiceScheme
		}
		hostport := s.Host
		switch {
		case s.Port > 0:
			hostport = net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
		case strings.Contains(s.Host, ":"):
			hostport = "[" + s.Host + "]"
		}
		parts = append(parts, scheme+"://"+hostport)
	}
	if len(s.GPUs) > 0 {
		idx := make([]string, len(s.GPUs))
		for i, g := range s.GPUs {
			idx[i] = strconv.Itoa(g.Index)
		}
		parts = append(parts, "GPUs "+strings.Join(idx, ","))
	}
	if s.ReadyAt != nil {
		parts = append(parts, "ready since "+s.ReadyAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, "  ")
}

// ApplyShownServiceState rewrites the state of t.ServiceStatus to what the
// server shows: ServiceStateStopped when the task is not RUNNING,
// ServiceStateLost when it is RUNNING with service_lost_at set (its worker is
// silent), ServiceStateStopping when it is RUNNING with a stop request;
// otherwise the reported state stays. The store applies it to every task it
// reads; the stored document is not changed. Nothing happens without a
// status.
func (t *Task) ApplyShownServiceState() {
	st := t.ServiceStatus
	if st == nil {
		return
	}
	switch {
	case t.State != TaskStateRunning:
		st.State = ServiceStateStopped
	case t.ServiceLostAt != nil:
		st.State = ServiceStateLost
	case t.ServiceStopRequestedAt != nil:
		st.State = ServiceStateStopping
	}
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
