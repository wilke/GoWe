package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// gpuInventoryTimeout bounds the nvidia-smi query run at startup (#323).
const gpuInventoryTimeout = 10 * time.Second

// nvidiaSMIQueryArgs asks nvidia-smi for one "index, uuid, name, memory.total"
// CSV line per GPU, memory in MiB.
var nvidiaSMIQueryArgs = []string{"--query-gpu=index,uuid,name,memory.total", "--format=csv,noheader,nounits"}

// collectGPUInventory queries nvidia-smi and returns the GPUs selected by
// deviceIDs (--gpu-id). It never fails: problems are logged at WARN and an
// empty, non-nil list is returned, so GPU metadata cannot block registration.
func collectGPUInventory(ctx context.Context, logger *slog.Logger, deviceIDs string) []model.GPUInfo {
	out, err := runNvidiaSMI(ctx)
	if err != nil {
		logger.Warn("GPU inventory unavailable, registering without it", "error", err)
		return []model.GPUInfo{}
	}
	gpus, errs := parseNvidiaSMICSV(string(out))
	for _, e := range errs {
		logger.Warn("GPU inventory: skipping nvidia-smi line", "error", e)
	}
	return filterGPUsByID(gpus, deviceIDs)
}

// runNvidiaSMI runs the nvidia-smi inventory query with gpuInventoryTimeout.
func runNvidiaSMI(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, gpuInventoryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nvidia-smi", nvidiaSMIQueryArgs...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("nvidia-smi: %w", ctx.Err())
		}
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	return out, nil
}

// parseNvidiaSMICSV parses nvidia-smi "index, uuid, name, memory.total" CSV
// output (noheader, nounits). Blank lines are ignored; lines that cannot be
// parsed are skipped and returned as errors.
func parseNvidiaSMICSV(out string) ([]model.GPUInfo, []error) {
	var gpus []model.GPUInfo
	var errs []error
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		g, err := parseNvidiaSMILine(line)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		gpus = append(gpus, g)
	}
	return gpus, errs
}

// parseNvidiaSMILine parses one CSV line. The name is the only free-text
// field, so any extra commas are taken to belong to it.
func parseNvidiaSMILine(line string) (model.GPUInfo, error) {
	fields := strings.Split(line, ",")
	if len(fields) < 4 {
		return model.GPUInfo{}, fmt.Errorf("line %q: want 4 fields, got %d", line, len(fields))
	}
	index, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil || index < 0 {
		return model.GPUInfo{}, fmt.Errorf("line %q: invalid index %q", line, strings.TrimSpace(fields[0]))
	}
	uuid := strings.TrimSpace(fields[1])
	if uuid == "" {
		return model.GPUInfo{}, fmt.Errorf("line %q: empty uuid", line)
	}
	memField := strings.TrimSpace(fields[len(fields)-1])
	memMB, err := strconv.ParseInt(memField, 10, 64)
	if err != nil || memMB < 0 {
		return model.GPUInfo{}, fmt.Errorf("line %q: invalid memory.total %q", line, memField)
	}
	return model.GPUInfo{
		Index:      index,
		UUID:       uuid,
		Name:       strings.TrimSpace(strings.Join(fields[2:len(fields)-1], ",")),
		MemTotalMB: memMB,
	}, nil
}

// filterGPUsByID keeps the GPUs listed in deviceIDs, the comma-separated
// --gpu-id value. Entries are GPU indices or GPU UUIDs ("GPU-..."), the two
// forms Docker and CUDA_VISIBLE_DEVICES accept. An empty deviceIDs keeps every
// GPU; entries that match no GPU are ignored. The result is never nil.
func filterGPUsByID(gpus []model.GPUInfo, deviceIDs string) []model.GPUInfo {
	out := make([]model.GPUInfo, 0, len(gpus))
	if strings.TrimSpace(deviceIDs) == "" {
		return append(out, gpus...)
	}
	wantIndex := make(map[int]bool)
	wantUUID := make(map[string]bool)
	for _, part := range strings.Split(deviceIDs, ",") {
		part = strings.TrimSpace(part)
		if n, err := strconv.Atoi(part); err == nil {
			wantIndex[n] = true
		} else if part != "" {
			wantUUID[part] = true
		}
	}
	for _, g := range gpus {
		if wantIndex[g.Index] || wantUUID[g.UUID] {
			out = append(out, g)
		}
	}
	return out
}
