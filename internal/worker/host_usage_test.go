package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func usageF64(v float64) *float64 { return &v }
func usageI64(v int64) *int64     { return &v }

// usageJSON renders a value as JSON so failures with pointers are readable
// and time values compare by their encoding.
func usageJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseLoadavg(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		load1, load5 float64
		wantErr      bool
	}{
		{"real", "0.52 0.58 0.59 1/1234 5678\n", 0.52, 0.58, false},
		{"high load", "193.10 180.00 150.50 200/3000 1\n", 193.10, 180.00, false},
		{"too few fields", "0.52\n", 0, 0, true},
		{"non-numeric", "x 0.58 0.59 1/1 1\n", 0, 0, true},
		{"negative", "-1 0.58 0.59 1/1 1\n", 0, 0, true},
		{"empty", "", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l1, l5, err := parseLoadavg(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if l1 != tt.load1 || l5 != tt.load5 {
				t.Errorf("load = %v/%v, want %v/%v", l1, l5, tt.load1, tt.load5)
			}
		})
	}
}

func TestParseMeminfo(t *testing.T) {
	procMeminfo := "MemTotal:       16318412 kB\nMemFree:         1234567 kB\nMemAvailable:    8159206 kB\nBuffers:          123456 kB\n"
	tests := []struct {
		name         string
		in           string
		total, avail int64
		wantErr      bool
	}{
		{"real", procMeminfo, 15935, 7967, false},
		{"no MemAvailable", "MemTotal: 16318412 kB\nMemFree: 1234567 kB\n", 0, 0, true},
		{"malformed value", "MemTotal: abc kB\nMemAvailable: 1 kB\n", 0, 0, true},
		{"unexpected unit", "MemTotal: 1 MB\nMemAvailable: 1 kB\n", 0, 0, true},
		{"empty", "", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, avail, err := parseMeminfo(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if total != tt.total || avail != tt.avail {
				t.Errorf("mem = %d/%d MiB, want %d/%d", total, avail, tt.total, tt.avail)
			}
		})
	}
}

func TestParsePSISomeAvg10(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    float64
		wantErr bool
	}{
		{"memory with full line", "some avg10=1.23 avg60=0.80 avg300=0.10 total=123456\nfull avg10=0.50 avg60=0.20 avg300=0.05 total=6543\n", 1.23, false},
		{"cpu some only", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", 0, false},
		{"no some line", "full avg10=1.00 avg60=0.00 avg300=0.00 total=0\n", 0, true},
		{"no avg10", "some avg60=0.80 total=1\n", 0, true},
		{"bad value", "some avg10=x avg60=0.80 avg300=0.10 total=1\n", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePSISomeAvg10(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("avg10 = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseNvidiaSMIUsageCSV(t *testing.T) {
	inventory := []model.GPUInfo{{Index: 0, UUID: "GPU-a"}, {Index: 1, UUID: "GPU-b"}}
	tests := []struct {
		name     string
		out      string
		want     []model.GPUUsage
		wantErrs int
	}{
		{"two gpus", "GPU-a, 1024, 42\nGPU-b, 0, 0\n", []model.GPUUsage{
			{UUID: "GPU-a", MemUsedMB: usageI64(1024), UtilPct: usageF64(42)},
			{UUID: "GPU-b", MemUsedMB: usageI64(0), UtilPct: usageF64(0)},
		}, 0},
		{"[N/A] makes the field absent", "GPU-a, [N/A], [N/A]\nGPU-b, 512, [N/A]\n", []model.GPUUsage{
			{UUID: "GPU-a"},
			{UUID: "GPU-b", MemUsedMB: usageI64(512)},
		}, 0},
		{"blank lines and CRLF", "\nGPU-a, 1, 2\r\n\n", []model.GPUUsage{
			{UUID: "GPU-a", MemUsedMB: usageI64(1), UtilPct: usageF64(2)},
		}, 0},
		{"malformed lines skipped", "garbage\nGPU-a, 1\n, 1, 2\nGPU-b, 3, 4\n", []model.GPUUsage{
			{UUID: "GPU-b", MemUsedMB: usageI64(3), UtilPct: usageF64(4)},
		}, 3},
		{"unknown uuid filtered", "GPU-x, 1, 2\nGPU-a, 5, 6\n", []model.GPUUsage{
			{UUID: "GPU-a", MemUsedMB: usageI64(5), UtilPct: usageF64(6)},
		}, 0},
		{"negative and non-finite values absent", "GPU-a, -1, NaN\n", []model.GPUUsage{{UUID: "GPU-a"}}, 0},
		{"empty output", "", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := parseNvidiaSMIUsageCSV(tt.out, inventory)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("usage = %s, want %s", usageJSON(t, got), usageJSON(t, tt.want))
			}
			if len(errs) != tt.wantErrs {
				t.Errorf("errors = %v, want %d", errs, tt.wantErrs)
			}
		})
	}
}

// writeUsageFixture writes content to dir/name and returns the path.
func writeUsageFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUsageSampler_SampleOnce(t *testing.T) {
	dir := t.TempDir()
	full := usagePaths{
		loadavg:   writeUsageFixture(t, dir, "loadavg", "1.50 0.75 0.25 3/1000 42\n"),
		meminfo:   writeUsageFixture(t, dir, "meminfo", "MemTotal: 2097152 kB\nMemAvailable: 1048576 kB\n"),
		psiCPU:    writeUsageFixture(t, dir, "cpu", "some avg10=2.50 avg60=1.00 avg300=0.50 total=1\n"),
		psiMemory: writeUsageFixture(t, dir, "memory", "some avg10=0.10 avg60=0.00 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"),
	}
	noPSI := full
	noPSI.psiCPU, noPSI.psiMemory = filepath.Join(dir, "absent-cpu"), filepath.Join(dir, "absent-memory")
	missing := usagePaths{
		loadavg:   filepath.Join(dir, "absent-loadavg"),
		meminfo:   filepath.Join(dir, "absent-meminfo"),
		psiCPU:    filepath.Join(dir, "absent-cpu"),
		psiMemory: filepath.Join(dir, "absent-memory"),
	}

	now := time.Date(2026, 11, 24, 10, 0, 0, 500, time.UTC)
	sampledAt := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	host := &model.HostUsage{Load1: 1.5, Load5: 0.75, MemTotalMB: 2048, MemAvailableMB: 1024,
		PSICPUSomeAvg10: usageF64(2.5), PSIMemSomeAvg10: usageF64(0.1), SampledAt: sampledAt}
	hostNoPSI := &model.HostUsage{Load1: 1.5, Load5: 0.75, MemTotalMB: 2048, MemAvailableMB: 1024, SampledAt: sampledAt}
	inventory := []model.GPUInfo{{Index: 0, UUID: "GPU-a", MemTotalMB: 81920}}

	smiOK := func(context.Context) ([]byte, error) { return []byte("GPU-a, 100, 50\nGPU-z, 1, 1\n"), nil }
	smiFail := func(context.Context) ([]byte, error) { return nil, errors.New("nvidia-smi: not found") }

	tests := []struct {
		name   string
		paths  usagePaths
		gpus   []model.GPUInfo
		runSMI func(context.Context) ([]byte, error)
		want   *model.WorkerUsage
	}{
		{"host and gpu", full, inventory, smiOK, &model.WorkerUsage{Host: host,
			GPUs: []model.GPUUsage{{UUID: "GPU-a", MemUsedMB: usageI64(100), UtilPct: usageF64(50)}}}},
		{"no inventory skips nvidia-smi", full, nil, nil, &model.WorkerUsage{Host: host}},
		{"nvidia-smi failure omits gpu usage", full, inventory, smiFail, &model.WorkerUsage{Host: host}},
		{"no pressure files", noPSI, nil, nil, &model.WorkerUsage{Host: hostNoPSI}},
		{"no proc files", missing, nil, nil, &model.WorkerUsage{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runSMI := tt.runSMI
			if runSMI == nil {
				runSMI = func(context.Context) ([]byte, error) {
					t.Error("nvidia-smi must not run without a GPU inventory")
					return nil, errors.New("unexpected")
				}
			}
			s := &usageSampler{
				paths:  tt.paths,
				gpus:   tt.gpus,
				runSMI: runSMI,
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				now:    func() time.Time { return now },
			}
			if got := s.latest(); got != nil {
				t.Fatalf("latest() before sampling = %s, want nil", usageJSON(t, got))
			}
			s.sampleOnce(context.Background())
			got := s.latest()
			if got == nil {
				t.Fatal("latest() after sampling = nil")
			}
			if g, w := usageJSON(t, got), usageJSON(t, tt.want); g != w {
				t.Errorf("sample = %s, want %s", g, w)
			}
		})
	}
}

func TestUsageSampler_GPUWarnRateLimited(t *testing.T) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	s := &usageSampler{
		paths:  usagePaths{},
		gpus:   []model.GPUInfo{{Index: 0, UUID: "GPU-a"}},
		runSMI: func(context.Context) ([]byte, error) { return nil, errors.New("boom") },
		logger: slog.New(slog.NewTextHandler(&buf, nil)),
		now:    func() time.Time { return now },
	}
	steps := []struct {
		advance   time.Duration
		wantWarns int
	}{
		{0, 1},
		{time.Minute, 1},
		{9 * time.Minute, 2},
		{time.Minute, 2},
	}
	for i, st := range steps {
		now = now.Add(st.advance)
		s.sampleOnce(context.Background())
		if n := strings.Count(buf.String(), "GPU usage unavailable"); n != st.wantWarns {
			t.Errorf("after sample %d: %d WARNs, want %d", i+1, n, st.wantWarns)
		}
	}
}

func TestNilUsageSampler_Latest(t *testing.T) {
	var s *usageSampler
	if got := s.latest(); got != nil {
		t.Errorf("nil sampler latest() = %v, want nil", got)
	}
}

func TestClientHeartbeat_Usage(t *testing.T) {
	host := &model.HostUsage{Load1: 1, Load5: 2, MemTotalMB: 4096, MemAvailableMB: 1024,
		SampledAt: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	gpus := []model.GPUUsage{{UUID: "GPU-a", MemUsedMB: usageI64(10), UtilPct: usageF64(5)}}
	tests := []struct {
		name     string
		usage    *model.WorkerUsage
		wantHost bool
		wantGPUs bool
	}{
		{"sample with host and gpus", &model.WorkerUsage{Host: host, GPUs: gpus}, true, true},
		{"host only", &model.WorkerUsage{Host: host}, true, false},
		{"no sample yet", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok","data":{"worker_id":"wrk_1","state":"online"}}`))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, nil)
			if _, err := c.Heartbeat(context.Background(), []string{"task_1"}, tt.usage); err != nil {
				t.Fatalf("Heartbeat: %v", err)
			}
			if got := body["running_tasks"]; !reflect.DeepEqual(got, []any{"task_1"}) {
				t.Errorf("running_tasks = %v, want [task_1]", got)
			}
			if _, ok := body["host"]; ok != tt.wantHost {
				t.Errorf("host present = %v, want %v (body %v)", ok, tt.wantHost, body)
			}
			if _, ok := body["gpu_usage"]; ok != tt.wantGPUs {
				t.Errorf("gpu_usage present = %v, want %v (body %v)", ok, tt.wantGPUs, body)
			}
			if tt.wantHost {
				h, _ := body["host"].(map[string]any)
				if h["load1"] != float64(1) || h["mem_total_mb"] != float64(4096) || h["sampled_at"] != "2026-11-24T10:00:00Z" {
					t.Errorf("host = %v", h)
				}
			}
		})
	}
}
