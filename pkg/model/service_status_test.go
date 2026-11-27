package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// u renders code point c. Control, format and separator characters are
// built with it instead of being written into string literals (most are
// invisible in an editor).
func u(c rune) string { return string(c) }

// validStatus returns a ready status with every field set.
func validStatus(now time.Time) ServiceStatus {
	ready := now.Add(-time.Minute)
	return ServiceStatus{
		State: ServiceStateReady, Host: "gpu01.example.org", Port: 8000, Scheme: "http",
		GPUs:        []ServiceGPU{{UUID: "GPU-1a2b3c4d-0000-1111-2222-333344445555", Index: 0}, {Index: 3, MemBudgetMB: 40000}},
		ImageSHA256: strings.Repeat("ab", 32), PID: 4242, ReadyAt: &ready, Detail: "ok",
	}
}

func TestServiceStatus_Validate(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	tp := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	tests := []struct {
		name    string
		mod     func(s *ServiceStatus)
		wantErr string // "" = valid
	}{
		{"all fields", func(s *ServiceStatus) {}, ""},
		{"minimal: state only", func(s *ServiceStatus) { *s = ServiceStatus{State: ServiceStateStarting} }, ""},
		{"state unhealthy", func(s *ServiceStatus) { s.State = ServiceStateUnhealthy }, ""},
		{"state stopping", func(s *ServiceStatus) { s.State = ServiceStateStopping }, ""},
		{"state empty", func(s *ServiceStatus) { s.State = "" }, "state must be"},
		{"state READY (case)", func(s *ServiceStatus) { s.State = "READY" }, "state must be"},
		{"state lost (shown only)", func(s *ServiceStatus) { s.State = ServiceStateLost }, "state must be"},
		{"state stopped (shown only)", func(s *ServiceStatus) { s.State = ServiceStateStopped }, "state must be"},
		{"long state not echoed in full", func(s *ServiceStatus) { s.State = strings.Repeat("x", 100) }, `"` + strings.Repeat("x", 32) + `"...`},

		{"host empty", func(s *ServiceStatus) { s.Host = "" }, ""},
		{"host single label", func(s *ServiceStatus) { s.Host = "localhost" }, ""},
		{"host with hyphen and digits", func(s *ServiceStatus) { s.Host = "gpu-01.node7.cluster" }, ""},
		{"host IPv4", func(s *ServiceStatus) { s.Host = "10.1.2.3" }, ""},
		{"host IPv6", func(s *ServiceStatus) { s.Host = "fd00::1" }, ""},
		{"host IPv6 loopback", func(s *ServiceStatus) { s.Host = "::1" }, ""},
		{"host IPv4-mapped IPv6", func(s *ServiceStatus) { s.Host = "::ffff:10.1.2.3" }, ""},
		{"host 253 bytes", func(s *ServiceStatus) { s.Host = longHost(253) }, ""},
		{"host 254 bytes", func(s *ServiceStatus) { s.Host = longHost(254) }, "longer than 253"},
		{"host bracketed IPv6", func(s *ServiceStatus) { s.Host = "[fd00::1]" }, "not an IPv6 address"},
		{"host IPv6 with zone", func(s *ServiceStatus) { s.Host = "fe80::1%eth0" }, "not an IPv6 address"},
		{"host with port", func(s *ServiceStatus) { s.Host = "gpu01:8000" }, "not an IPv6 address"},
		{"host IPv4 with port", func(s *ServiceStatus) { s.Host = "10.1.2.3:8000" }, "not an IPv6 address"},
		{"host bad IPv4", func(s *ServiceStatus) { s.Host = "10.1.2.300" }, "neither a dotted-decimal IPv4"},
		{"host IPv4 short form", func(s *ServiceStatus) { s.Host = "10.1" }, "neither a dotted-decimal IPv4"},
		{"host IPv4 leading zero", func(s *ServiceStatus) { s.Host = "010.1.2.3" }, "neither a dotted-decimal IPv4"},
		{"host with space", func(s *ServiceStatus) { s.Host = "gpu 01" }, "printable ASCII"},
		{"host with control", func(s *ServiceStatus) { s.Host = "gpu" + u(10) + "01" }, "printable ASCII"},
		{"host non-ASCII", func(s *ServiceStatus) { s.Host = "gp" + u(0xfc) + "01" }, "printable ASCII"},
		{"host with slash", func(s *ServiceStatus) { s.Host = "gpu01/x" }, "letters, digits"},
		{"host with at", func(s *ServiceStatus) { s.Host = "user@gpu01" }, "letters, digits"},
		{"host URL", func(s *ServiceStatus) { s.Host = "http://gpu01" }, "not an IPv6 address"},
		{"host underscore", func(s *ServiceStatus) { s.Host = "gpu_01" }, "letters, digits"},
		{"host leading hyphen", func(s *ServiceStatus) { s.Host = "-gpu01" }, "start or end"},
		{"host trailing hyphen label", func(s *ServiceStatus) { s.Host = "gpu01-.example" }, "start or end"},
		{"host empty label", func(s *ServiceStatus) { s.Host = "gpu01..example" }, "no empty label"},
		{"host trailing dot", func(s *ServiceStatus) { s.Host = "gpu01.example." }, "no empty label"},
		{"host label 64 bytes", func(s *ServiceStatus) { s.Host = strings.Repeat("a", 64) + ".example" }, "1-63"},
		{"host numeric last label", func(s *ServiceStatus) { s.Host = "gpu01.123" }, "all digits"},
		{"host 0x last label", func(s *ServiceStatus) { s.Host = "0x7f000001" }, "hexadecimal number"},
		{"host 0X last label", func(s *ServiceStatus) { s.Host = "0X7F000001" }, "hexadecimal number"},
		{"host 0x last label after others", func(s *ServiceStatus) { s.Host = "gpu01.0xff" }, "hexadecimal number"},
		{"host bare 0x last label", func(s *ServiceStatus) { s.Host = "gpu01.0x" }, "hexadecimal number"},
		{"host 0x label not last", func(s *ServiceStatus) { s.Host = "0x7f.example" }, ""},
		{"host 0x then non-hex", func(s *ServiceStatus) { s.Host = "0xgpu" }, ""},
		{"host single all-hex label", func(s *ServiceStatus) { s.Host = "7f000001" }, "all hexadecimal digits"},
		{"host single all-hex label, letters only", func(s *ServiceStatus) { s.Host = "deadbeef" }, "all hexadecimal digits"},
		{"host single all-hex label, upper case", func(s *ServiceStatus) { s.Host = "CAFE" }, "all hexadecimal digits"},
		{"host single label with a non-hex letter", func(s *ServiceStatus) { s.Host = "coconut" }, ""},
		{"host all-hex last label after others", func(s *ServiceStatus) { s.Host = "gpu01.cafe" }, ""},
		{"host all-hex labels, two of them", func(s *ServiceStatus) { s.Host = "dead.beef" }, ""},

		{"port 0", func(s *ServiceStatus) { s.Port = 0 }, ""},
		{"port 1", func(s *ServiceStatus) { s.Port = 1 }, ""},
		{"port 65535", func(s *ServiceStatus) { s.Port = 65535 }, ""},
		{"port 65536", func(s *ServiceStatus) { s.Port = 65536 }, "port must be"},
		{"port negative", func(s *ServiceStatus) { s.Port = -1 }, "port must be"},

		{"scheme empty", func(s *ServiceStatus) { s.Scheme = "" }, ""},
		{"scheme https", func(s *ServiceStatus) { s.Scheme = "https" }, ""},
		{"scheme HTTP", func(s *ServiceStatus) { s.Scheme = "HTTP" }, "scheme must be"},
		{"scheme grpc", func(s *ServiceStatus) { s.Scheme = "grpc" }, "scheme must be"},

		{"64 gpus", func(s *ServiceStatus) { s.GPUs = make([]ServiceGPU, 64) }, ""},
		{"65 gpus", func(s *ServiceStatus) { s.GPUs = make([]ServiceGPU, 65) }, "at most 64 gpus"},
		{"gpu uuid 96 bytes", func(s *ServiceStatus) { s.GPUs[0].UUID = strings.Repeat("a", 96) }, ""},
		{"gpu uuid 97 bytes", func(s *ServiceStatus) { s.GPUs[0].UUID = strings.Repeat("a", 97) }, "longer than 96"},
		{"gpu uuid with space", func(s *ServiceStatus) { s.GPUs[1].UUID = "GPU 1" }, "gpus[1].uuid must contain only printable"},
		{"gpu index 1023", func(s *ServiceStatus) { s.GPUs[0].Index = 1023 }, ""},
		{"gpu index 1024", func(s *ServiceStatus) { s.GPUs[0].Index = 1024 }, "gpus[0].index"},
		{"gpu index negative", func(s *ServiceStatus) { s.GPUs[0].Index = -1 }, "gpus[0].index"},
		{"gpu mem budget negative", func(s *ServiceStatus) { s.GPUs[1].MemBudgetMB = -1 }, "mem_budget_mb"},

		{"image sha empty", func(s *ServiceStatus) { s.ImageSHA256 = "" }, ""},
		{"image sha upper case", func(s *ServiceStatus) { s.ImageSHA256 = strings.Repeat("AB", 32) }, "image_sha256"},
		{"image sha 63 chars", func(s *ServiceStatus) { s.ImageSHA256 = strings.Repeat("a", 63) }, "image_sha256"},
		{"image sha 65 chars", func(s *ServiceStatus) { s.ImageSHA256 = strings.Repeat("a", 65) }, "image_sha256"},
		{"image sha with prefix", func(s *ServiceStatus) { s.ImageSHA256 = "sha256:" + strings.Repeat("a", 57) }, "image_sha256"},
		{"image sha non-hex", func(s *ServiceStatus) { s.ImageSHA256 = strings.Repeat("g", 64) }, "image_sha256"},

		{"pid 0", func(s *ServiceStatus) { s.PID = 0 }, ""},
		{"pid negative", func(s *ServiceStatus) { s.PID = -1 }, "pid"},

		{"detail 512 bytes", func(s *ServiceStatus) { s.Detail = strings.Repeat("d", 512) }, ""},
		{"detail 513 bytes", func(s *ServiceStatus) { s.Detail = strings.Repeat("d", 513) }, "longer than 512"},
		{"detail with control (not normalised)", func(s *ServiceStatus) { s.Detail = "a" + u(9) + "b" }, "control characters"},

		{"ready_at absent", func(s *ServiceStatus) { s.ReadyAt = nil }, ""},
		{"ready_at in 5 minutes", func(s *ServiceStatus) { s.ReadyAt = tp(5 * time.Minute) }, ""},
		{"ready_at in 5 minutes 1 second", func(s *ServiceStatus) { s.ReadyAt = tp(5*time.Minute + time.Second) }, "in the future"},
		{"ready_at zero", func(s *ServiceStatus) { s.ReadyAt = &time.Time{} }, "zero time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validStatus(now)
			tt.mod(&s)
			err := s.Validate(now)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}

	var nilStatus *ServiceStatus
	if err := nilStatus.Validate(now); err == nil {
		t.Error("nil status: Validate = nil, want an error")
	}
}

// longHost returns a valid DNS name of exactly n bytes (labels of up to 63).
func longHost(n int) string {
	var b strings.Builder
	for b.Len() < n {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		l := min(63, n-b.Len())
		if l == 0 {
			l = 1
		}
		b.WriteString(strings.Repeat("a", l))
	}
	return b.String()[:n]
}

func TestServiceStatus_Normalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain text unchanged", "health check failed: 503", "health check failed: 503"},
		{"tab and newlines become spaces", "line1" + u(10) + "line2" + u(13) + u(9) + "x", "line1 line2  x"},
		{"NUL and DEL become spaces", "a" + u(0) + "b" + u(0x7f) + "c", "a b c"},
		{"C1 control becomes a space", "a" + u(0x85) + "b", "a b"},
		{"invalid UTF-8 replaced", string([]byte{'a', 0xff, 'b'}), "a" + u(0xfffd) + "b"},
		{"non-ASCII text kept", u(0xe9) + "tat: pr" + u(0xea) + "t", u(0xe9) + "tat: pr" + u(0xea) + "t"},
		{"bidi override (Cf) becomes a space", "abc" + u(0x202e) + "def", "abc def"},
		{"bidi isolates (Cf) become spaces", "a" + u(0x2066) + "b" + u(0x2069) + "c", "a b c"},
		{"zero-width space and joiner (Cf) become spaces", "a" + u(0x200b) + "b" + u(0x200d) + "c", "a b c"},
		{"byte order mark (Cf) becomes a space", u(0xfeff) + "ok", " ok"},
		{"soft hyphen (Cf) becomes a space", "a" + u(0x00ad) + "b", "a b"},
		{"line separator (Zl) becomes a space", "a" + u(0x2028) + "b", "a b"},
		{"paragraph separator (Zp) becomes a space", "a" + u(0x2029) + "b", "a b"},
		{"other spaces (Zs) kept", "a" + u(0x00a0) + "b" + u(0x3000) + "c", "a" + u(0x00a0) + "b" + u(0x3000) + "c"},
	}
	now := time.Now().UTC()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ServiceStatus{State: ServiceStateStarting, Detail: tt.in}
			s.Normalize()
			if s.Detail != tt.want {
				t.Fatalf("Detail = %q, want %q", s.Detail, tt.want)
			}
			if err := s.Validate(now); err != nil {
				t.Errorf("Validate after Normalize = %v, want nil", err)
			}
		})
	}
	var nilStatus *ServiceStatus
	nilStatus.Normalize() // must not panic
}

func TestServiceStatus_Line(t *testing.T) {
	ready := time.Date(2026, 11, 27, 10, 0, 0, 0, time.FixedZone("x", 3600))
	// Every status here was updated at now: none is stale.
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		st   *ServiceStatus
		want string
	}{
		{"nil", nil, ""},
		{"state only", &ServiceStatus{State: ServiceStateStarting, UpdatedAt: now}, "starting"},
		{"full", &ServiceStatus{State: ServiceStateReady, Host: "gpu01", Port: 8000, Scheme: "https",
			GPUs: []ServiceGPU{{Index: 0}, {Index: 3}}, ReadyAt: &ready, UpdatedAt: now},
			"ready  https://gpu01:8000  GPUs 0,3  ready since 2026-11-27T09:00:00Z"},
		{"scheme defaults to http", &ServiceStatus{State: ServiceStateReady, Host: "10.0.0.1", Port: 80, UpdatedAt: now}, "ready  http://10.0.0.1:80"},
		{"IPv6 bracketed", &ServiceStatus{State: ServiceStateReady, Host: "fd00::1", Port: 8000, UpdatedAt: now}, "ready  http://[fd00::1]:8000"},
		{"IPv6 without port", &ServiceStatus{State: ServiceStateReady, Host: "fd00::1", UpdatedAt: now}, "ready  http://[fd00::1]"},
		{"no host", &ServiceStatus{State: ServiceStateUnhealthy, Port: 8000, UpdatedAt: now}, "unhealthy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.st.Line(now); got != tt.want {
				t.Errorf("Line = %q, want %q", got, tt.want)
			}
		})
	}
}

// Line marks a status in a state a worker reports with the time it was last
// reported once that is more than ServiceStatusStaleAfter ago; never the
// states the server shows in its place (lost, stopped).
func TestServiceStatus_LineStale(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	updated := func(ago time.Duration) time.Time {
		return now.Add(-ago).In(time.FixedZone("x", 3600)) // rendered in UTC
	}
	tests := []struct {
		name  string
		state string
		ago   time.Duration
		stale bool
	}{
		{"starting, fresh", ServiceStateStarting, 10 * time.Second, false},
		{"ready, exactly the limit", ServiceStateReady, ServiceStatusStaleAfter, false},
		{"starting, stale", ServiceStateStarting, 3 * time.Minute, true},
		{"ready, stale", ServiceStateReady, 3 * time.Minute, true},
		{"unhealthy, stale", ServiceStateUnhealthy, 3 * time.Minute, true},
		{"stopping, stale", ServiceStateStopping, 3 * time.Minute, true},
		{"lost, stale", ServiceStateLost, 3 * time.Minute, false},
		{"stopped, stale", ServiceStateStopped, 3 * time.Minute, false},
		{"lost, long ago", ServiceStateLost, 48 * time.Hour, false},
		{"stopped, long ago", ServiceStateStopped, 48 * time.Hour, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &ServiceStatus{State: tt.state, Host: "gpu01", Port: 8000, UpdatedAt: updated(tt.ago)}
			want := tt.state + "  http://gpu01:8000"
			if tt.stale {
				want += " (last reported " + st.UpdatedAt.UTC().Format(time.RFC3339) + ")"
			}
			if got := st.Line(now); got != want {
				t.Errorf("Line = %q, want %q", got, want)
			}
		})
	}
	// The suffix, spelled out.
	st := &ServiceStatus{State: ServiceStateReady, UpdatedAt: updated(3 * time.Minute)}
	if got, want := st.Line(now), "ready (last reported 2026-11-27T11:57:00Z)"; got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
}

func TestTask_ApplyShownServiceState(t *testing.T) {
	at := time.Now().UTC()
	tests := []struct {
		name  string
		state TaskState
		lost  bool
		stop  bool
		want  string
	}{
		{"running", TaskStateRunning, false, false, ServiceStateReady},
		{"running, lost", TaskStateRunning, true, false, ServiceStateLost},
		{"running, stop requested", TaskStateRunning, false, true, ServiceStateStopping},
		{"running, lost and stop requested", TaskStateRunning, true, true, ServiceStateLost},
		{"failed", TaskStateFailed, false, false, ServiceStateStopped},
		{"skipped after a stop request", TaskStateSkipped, false, true, ServiceStateStopped},
		{"failed while lost", TaskStateFailed, true, false, ServiceStateStopped},
		{"queued", TaskStateQueued, false, false, ServiceStateStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := &Task{State: tt.state, ServiceStatus: &ServiceStatus{State: ServiceStateReady}}
			if tt.lost {
				tk.ServiceLostAt = &at
			}
			if tt.stop {
				tk.ServiceStopRequestedAt = &at
			}
			tk.ApplyShownServiceState()
			if tk.ServiceStatus.State != tt.want {
				t.Errorf("shown state = %q, want %q", tk.ServiceStatus.State, tt.want)
			}
		})
	}
	// No status: nothing to do.
	tk := &Task{State: TaskStateFailed}
	tk.ApplyShownServiceState()
	if tk.ServiceStatus != nil {
		t.Errorf("status = %+v, want nil", tk.ServiceStatus)
	}
}

// A ServiceReport is a flat JSON object: task_id beside the status fields.
func TestServiceReport_JSON(t *testing.T) {
	var r ServiceReport
	if err := json.Unmarshal([]byte(`{"task_id":"t1","state":"ready","host":"h","port":8000,"gpus":[{"index":2}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.TaskID != "t1" || r.State != ServiceStateReady || r.Host != "h" || r.Port != 8000 || len(r.GPUs) != 1 || r.GPUs[0].Index != 2 {
		t.Errorf("decoded = %+v", r)
	}
	b, err := json.Marshal(ServiceStatus{State: ServiceStateStarting})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"state":"starting","updated_at":"0001-01-01T00:00:00Z"}`; got != want {
		t.Errorf("marshal = %s, want %s", got, want)
	}
}
