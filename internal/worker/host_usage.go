package worker

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/pkg/model"
)

// usageSampleInterval is how often the worker samples host and GPU usage for
// its heartbeats (#323).
const usageSampleInterval = 15 * time.Second

// gpuUsageWarnInterval rate-limits the WARN logged when the GPU usage query
// fails.
const gpuUsageWarnInterval = 10 * time.Minute

// nvidiaSMIUsageArgs asks nvidia-smi for one "uuid, memory.used,
// utilization.gpu" CSV line per GPU, memory in MiB and utilisation in percent.
var nvidiaSMIUsageArgs = []string{"--query-gpu=uuid,memory.used,utilization.gpu", "--format=csv,noheader,nounits"}

// usagePaths are the files a host usage sample is read from; tests point them
// at fixtures.
type usagePaths struct {
	loadavg   string
	meminfo   string
	psiCPU    string
	psiMemory string
}

// defaultUsagePaths are the Linux sources. On other systems the files are
// missing and the host sample is simply empty.
var defaultUsagePaths = usagePaths{
	loadavg:   "/proc/loadavg",
	meminfo:   "/proc/meminfo",
	psiCPU:    "/proc/pressure/cpu",
	psiMemory: "/proc/pressure/memory",
}

// usageSampler periodically samples host and GPU usage and keeps the latest
// sample for the heartbeat loop, which never waits for a sample to be taken.
type usageSampler struct {
	paths  usagePaths
	gpus   []model.GPUInfo
	runSMI func(ctx context.Context) ([]byte, error)
	logger *slog.Logger
	now    func() time.Time

	// lastGPUWarn is used only by sampleOnce, which runs serially.
	lastGPUWarn time.Time

	mu     sync.Mutex
	sample *model.WorkerUsage
}

// newUsageSampler returns a sampler reading the Linux /proc files. GPU usage
// is sampled only when gpus, the inventory reported at registration, is
// non-empty, and only for the GPUs in it.
func newUsageSampler(logger *slog.Logger, gpus []model.GPUInfo) *usageSampler {
	return &usageSampler{
		paths:  defaultUsagePaths,
		gpus:   gpus,
		runSMI: func(ctx context.Context) ([]byte, error) { return runNvidiaSMI(ctx, nvidiaSMIUsageArgs...) },
		logger: logger,
		now:    time.Now,
	}
}

// run samples once immediately, then every usageSampleInterval until ctx is
// cancelled.
func (s *usageSampler) run(ctx context.Context) {
	s.sampleOnce(ctx)
	ticker := time.NewTicker(usageSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleOnce(ctx)
		}
	}
}

// sampleOnce takes one sample and publishes it as the latest.
func (s *usageSampler) sampleOnce(ctx context.Context) {
	u := &model.WorkerUsage{Host: sampleHostUsage(s.paths, s.now())}
	if len(s.gpus) > 0 {
		u.GPUs = s.sampleGPUs(ctx)
	}
	s.mu.Lock()
	s.sample = u
	s.mu.Unlock()
}

// latest returns the most recent sample, or nil before the first one (and on
// a nil sampler). A published sample is never modified.
func (s *usageSampler) latest() *model.WorkerUsage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sample
}

// sampleGPUs runs the nvidia-smi usage query. On failure it returns nil and
// logs a WARN at most once per gpuUsageWarnInterval.
func (s *usageSampler) sampleGPUs(ctx context.Context) []model.GPUUsage {
	out, err := s.runSMI(ctx)
	if err != nil {
		if now := s.now(); s.lastGPUWarn.IsZero() || now.Sub(s.lastGPUWarn) >= gpuUsageWarnInterval {
			s.lastGPUWarn = now
			s.logger.Warn("GPU usage unavailable, heartbeats carry no GPU usage", "error", err)
		}
		return nil
	}
	usage, errs := parseNvidiaSMIUsageCSV(string(out), s.gpus)
	for _, e := range errs {
		s.logger.Debug("GPU usage: skipping nvidia-smi line", "error", e)
	}
	return usage
}

// sampleHostUsage reads load, memory and pressure from the files in p. A file
// that is missing or unparsable leaves its fields unset; nil is returned when
// nothing could be read (e.g. on a system without /proc).
func sampleHostUsage(p usagePaths, now time.Time) *model.HostUsage {
	var h model.HostUsage
	found := false
	if data, err := os.ReadFile(p.loadavg); err == nil {
		if l1, l5, err := parseLoadavg(string(data)); err == nil {
			h.Load1, h.Load5 = l1, l5
			found = true
		}
	}
	if data, err := os.ReadFile(p.meminfo); err == nil {
		if total, avail, err := parseMeminfo(string(data)); err == nil {
			h.MemTotalMB, h.MemAvailableMB = total, avail
			found = true
		}
	}
	h.PSICPUSomeAvg10 = readPSISomeAvg10(p.psiCPU)
	h.PSIMemSomeAvg10 = readPSISomeAvg10(p.psiMemory)
	if !found && h.PSICPUSomeAvg10 == nil && h.PSIMemSomeAvg10 == nil {
		return nil
	}
	h.SampledAt = now.UTC().Truncate(time.Second)
	return &h
}

// readPSISomeAvg10 returns the "some" avg10 of a /proc/pressure file, or nil
// when the file is absent (kernel without PSI) or unparsable.
func readPSISomeAvg10(path string) *float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := parsePSISomeAvg10(string(data))
	if err != nil {
		return nil
	}
	return &v
}

// parseLoadavg returns the 1- and 5-minute load averages from /proc/loadavg
// content ("0.52 0.58 0.59 1/1234 5678").
func parseLoadavg(s string) (load1, load5 float64, err error) {
	f := strings.Fields(s)
	if len(f) < 2 {
		return 0, 0, fmt.Errorf("loadavg %q: want at least 2 fields", s)
	}
	if load1, err = parseNonNegFloat(f[0]); err != nil {
		return 0, 0, fmt.Errorf("loadavg: load1: %w", err)
	}
	if load5, err = parseNonNegFloat(f[1]); err != nil {
		return 0, 0, fmt.Errorf("loadavg: load5: %w", err)
	}
	return load1, load5, nil
}

// parseMeminfo returns MemTotal and MemAvailable from /proc/meminfo content,
// converted from kB to MiB. Both lines are required.
func parseMeminfo(s string) (totalMB, availMB int64, err error) {
	total, avail := int64(-1), int64(-1)
	for _, line := range strings.Split(s, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var dst *int64
		switch strings.TrimSpace(key) {
		case "MemTotal":
			dst = &total
		case "MemAvailable":
			dst = &avail
		default:
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 || len(f) > 2 || (len(f) == 2 && f[1] != "kB") {
			return 0, 0, fmt.Errorf("meminfo: malformed line %q", line)
		}
		kb, perr := strconv.ParseInt(f[0], 10, 64)
		if perr != nil || kb < 0 {
			return 0, 0, fmt.Errorf("meminfo: malformed line %q", line)
		}
		*dst = kb / 1024
	}
	if total < 0 || avail < 0 {
		return 0, 0, fmt.Errorf("meminfo: MemTotal or MemAvailable missing")
	}
	return total, avail, nil
}

// parsePSISomeAvg10 returns the avg10 value of the "some" line of a
// /proc/pressure file ("some avg10=1.23 avg60=0.80 avg300=0.10 total=123").
func parsePSISomeAvg10(s string) (float64, error) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "some" {
			continue
		}
		for _, kv := range f[1:] {
			if v, ok := strings.CutPrefix(kv, "avg10="); ok {
				return parseNonNegFloat(v)
			}
		}
		return 0, fmt.Errorf("pressure: no avg10 in %q", line)
	}
	return 0, fmt.Errorf("pressure: no \"some\" line")
}

// parseNvidiaSMIUsageCSV parses nvidia-smi "uuid, memory.used,
// utilization.gpu" CSV output (noheader, nounits) and keeps only the GPUs in
// inventory. A value that is "[N/A]" or otherwise unparsable leaves that
// field nil; blank lines are ignored and malformed lines are skipped and
// returned as errors.
func parseNvidiaSMIUsageCSV(out string, inventory []model.GPUInfo) ([]model.GPUUsage, []error) {
	known := make(map[string]bool, len(inventory))
	for _, g := range inventory {
		known[g.UUID] = true
	}
	var usage []model.GPUUsage
	var errs []error
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) != 3 {
			errs = append(errs, fmt.Errorf("line %q: want 3 fields, got %d", line, len(fields)))
			continue
		}
		uuid := strings.TrimSpace(fields[0])
		if uuid == "" {
			errs = append(errs, fmt.Errorf("line %q: empty uuid", line))
			continue
		}
		if !known[uuid] {
			continue
		}
		u := model.GPUUsage{UUID: uuid}
		if mb, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil && mb >= 0 {
			u.MemUsedMB = &mb
		}
		if pct, err := parseNonNegFloat(strings.TrimSpace(fields[2])); err == nil {
			u.UtilPct = &pct
		}
		usage = append(usage, u)
	}
	return usage, errs
}

// parseNonNegFloat parses a finite, non-negative float.
func parseNonNegFloat(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	return v, nil
}
