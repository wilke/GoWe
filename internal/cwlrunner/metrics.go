package cwlrunner

import (
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// IterationMetrics holds metrics for a single scatter iteration.
type IterationMetrics struct {
	Index        int           `json:"index"`
	Duration     time.Duration `json:"duration_ns"`
	DurationStr  string        `json:"duration"`
	PeakMemoryKB int64         `json:"peak_memory_kb"`
	ExitCode     int           `json:"exit_code"`
	Status       string        `json:"status"`
}

// ScatterSummary holds aggregate statistics for scatter iterations.
type ScatterSummary struct {
	Count           int           `json:"count"`
	DurationAvg     time.Duration `json:"duration_avg_ns"`
	DurationAvgStr  string        `json:"duration_avg"`
	DurationStddev  time.Duration `json:"duration_stddev_ns"`
	DurationStddevStr string      `json:"duration_stddev"`
	MemoryAvgKB     int64         `json:"memory_avg_kb"`
	MemoryMaxKB     int64         `json:"memory_max_kb"`
	SuccessCount    int           `json:"success_count"`
	FailedCount     int           `json:"failed_count"`
}

// StepMetrics holds metrics for a single step/tool execution.
type StepMetrics struct {
	StepID       string        `json:"step_id"`
	ToolID       string        `json:"tool_id,omitempty"`
	StartTime    time.Time     `json:"start_time"`
	Duration     time.Duration `json:"duration_ns"`
	DurationStr  string        `json:"duration"`
	ExitCode     int           `json:"exit_code"`
	PeakMemoryKB int64         `json:"peak_memory_kb"`
	Status       string        `json:"status"` // "success", "failed", "skipped"

	// Scatter-specific fields
	Iterations     []IterationMetrics `json:"iterations,omitempty"`
	ScatterSummary *ScatterSummary    `json:"scatter_summary,omitempty"`
}

// WorkflowMetrics holds aggregate metrics for an entire workflow.
type WorkflowMetrics struct {
	WorkflowID    string        `json:"workflow_id,omitempty"`
	StartTime     time.Time     `json:"start_time"`
	Duration      time.Duration `json:"duration_ns"`
	DurationStr   string        `json:"duration"`
	TotalSteps    int           `json:"total_steps"`
	StepsComplete int           `json:"steps_completed"`
	StepsFailed   int           `json:"steps_failed"`
	StepsSkipped  int           `json:"steps_skipped"`
	Steps         []StepMetrics `json:"steps"`

	mu sync.Mutex // Protects Steps slice for concurrent access
}

// MetricsCollector collects metrics during workflow execution.
type MetricsCollector struct {
	enabled  bool
	workflow *WorkflowMetrics
}

// NewMetricsCollector creates a new metrics collector.
// If enabled is false, all operations are no-ops.
func NewMetricsCollector(enabled bool) *MetricsCollector {
	mc := &MetricsCollector{enabled: enabled}
	if enabled {
		mc.workflow = &WorkflowMetrics{
			StartTime: time.Now(),
			Steps:     make([]StepMetrics, 0),
		}
	}
	return mc
}

// SetWorkflowID sets the workflow ID for metrics.
func (mc *MetricsCollector) SetWorkflowID(id string) {
	if mc == nil || !mc.enabled || mc.workflow == nil {
		return
	}
	mc.workflow.WorkflowID = id
}

// SetTotalSteps sets the total number of steps in the workflow.
func (mc *MetricsCollector) SetTotalSteps(count int) {
	if mc == nil || !mc.enabled || mc.workflow == nil {
		return
	}
	mc.workflow.TotalSteps = count
}

// RecordStep records metrics for a completed step.
func (mc *MetricsCollector) RecordStep(metrics StepMetrics) {
	if mc == nil || !mc.enabled || mc.workflow == nil {
		return
	}

	// Set human-readable duration
	metrics.DurationStr = formatDuration(metrics.Duration)

	mc.workflow.mu.Lock()
	defer mc.workflow.mu.Unlock()

	mc.workflow.Steps = append(mc.workflow.Steps, metrics)

	switch metrics.Status {
	case "success":
		mc.workflow.StepsComplete++
	case "failed":
		mc.workflow.StepsFailed++
	case "skipped":
		mc.workflow.StepsSkipped++
	}
}

// Finalize completes the workflow metrics collection.
func (mc *MetricsCollector) Finalize() *WorkflowMetrics {
	if mc == nil || !mc.enabled || mc.workflow == nil {
		return nil
	}

	mc.workflow.Duration = time.Since(mc.workflow.StartTime)
	mc.workflow.DurationStr = formatDuration(mc.workflow.Duration)

	// Sort steps by start time for consistent output
	sort.Slice(mc.workflow.Steps, func(i, j int) bool {
		return mc.workflow.Steps[i].StartTime.Before(mc.workflow.Steps[j].StartTime)
	})

	return mc.workflow
}

// GetWorkflowMetrics returns the current workflow metrics (may be incomplete).
func (mc *MetricsCollector) GetWorkflowMetrics() *WorkflowMetrics {
	if mc == nil || !mc.enabled {
		return nil
	}
	return mc.workflow
}

// Enabled returns true if metrics collection is enabled.
func (mc *MetricsCollector) Enabled() bool {
	return mc != nil && mc.enabled
}

// ComputeScatterSummary computes aggregate statistics from iteration metrics.
func ComputeScatterSummary(iterations []IterationMetrics) *ScatterSummary {
	if len(iterations) == 0 {
		return nil
	}

	summary := &ScatterSummary{
		Count: len(iterations),
	}

	var totalDuration time.Duration
	var totalMemory int64
	for _, iter := range iterations {
		totalDuration += iter.Duration
		totalMemory += iter.PeakMemoryKB
		if iter.PeakMemoryKB > summary.MemoryMaxKB {
			summary.MemoryMaxKB = iter.PeakMemoryKB
		}
		if iter.Status == "success" {
			summary.SuccessCount++
		} else if iter.Status == "failed" {
			summary.FailedCount++
		}
	}

	// Compute averages
	n := len(iterations)
	summary.DurationAvg = totalDuration / time.Duration(n)
	summary.DurationAvgStr = formatDuration(summary.DurationAvg)
	summary.MemoryAvgKB = totalMemory / int64(n)

	// Compute standard deviation for duration
	if n > 1 {
		var sumSquaredDiff float64
		avgNs := float64(summary.DurationAvg.Nanoseconds())
		for _, iter := range iterations {
			diff := float64(iter.Duration.Nanoseconds()) - avgNs
			sumSquaredDiff += diff * diff
		}
		variance := sumSquaredDiff / float64(n)
		stddevNs := int64(math.Sqrt(variance))
		summary.DurationStddev = time.Duration(stddevNs)
		summary.DurationStddevStr = formatDuration(summary.DurationStddev)
	}

	return summary
}

// getResourceUsage extracts peak memory usage from process state.
// Returns peak RSS in KB. On Darwin (macOS), Maxrss is in bytes; on Linux, it's in KB.
func getResourceUsage(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}

	rusage, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || rusage == nil {
		return 0
	}

	// Darwin reports Maxrss in bytes, Linux reports in KB
	if runtime.GOOS == "darwin" {
		return rusage.Maxrss / 1024
	}
	return rusage.Maxrss
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
}

// formatMemory formats memory in KB to a human-readable string.
func formatMemory(kb int64) string {
	if kb == 0 {
		return "-"
	}
	if kb < 1024 {
		return fmt.Sprintf("%d KB", kb)
	}
	mb := float64(kb) / 1024
	if mb < 1024 {
		return fmt.Sprintf("%.1f MB", mb)
	}
	gb := mb / 1024
	return fmt.Sprintf("%.2f GB", gb)
}

// PrintMetricsSummary prints a formatted summary of workflow metrics.
func PrintMetricsSummary(w io.Writer, m *WorkflowMetrics) {
	if m == nil {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "=== Workflow Execution Summary ===")
	if m.WorkflowID != "" {
		fmt.Fprintf(w, "Workflow: %s\n", m.WorkflowID)
	}
	fmt.Fprintf(w, "Total Duration: %s\n", m.DurationStr)
	fmt.Fprintln(w)

	if len(m.Steps) > 0 {
		// Calculate column widths
		maxStepLen := 4 // "Step"
		for _, step := range m.Steps {
			if len(step.StepID) > maxStepLen {
				maxStepLen = len(step.StepID)
			}
		}
		if maxStepLen > 40 {
			maxStepLen = 40
		}

		// Print header
		fmt.Fprintf(w, "%-*s  %18s  %12s  %s\n", maxStepLen, "Step", "Duration", "Memory", "Status")
		fmt.Fprintln(w, strings.Repeat("-", maxStepLen+50))

		// Find peak memory step
		var peakMemoryKB int64
		var peakMemoryStep string
		for _, step := range m.Steps {
			stepPeakMem := step.PeakMemoryKB
			if step.ScatterSummary != nil {
				stepPeakMem = step.ScatterSummary.MemoryMaxKB
			}
			if stepPeakMem > peakMemoryKB {
				peakMemoryKB = stepPeakMem
				peakMemoryStep = step.StepID
			}
		}

		// Print steps
		for _, step := range m.Steps {
			stepID := step.StepID
			if len(stepID) > maxStepLen {
				stepID = stepID[:maxStepLen-3] + "..."
			}

			statusIcon := "✓"
			switch step.Status {
			case "failed":
				statusIcon = "✗"
			case "skipped":
				statusIcon = "○"
			}

			// Format duration and memory based on whether this is a scatter step
			var durationStr, memoryStr, statusStr string
			if step.ScatterSummary != nil {
				// Scatter step: show avg±stddev
				ss := step.ScatterSummary
				if ss.DurationStddev > 0 {
					durationStr = fmt.Sprintf("%s ± %s", ss.DurationAvgStr, ss.DurationStddevStr)
				} else {
					durationStr = ss.DurationAvgStr
				}
				memoryStr = formatMemory(ss.MemoryAvgKB)
				statusStr = fmt.Sprintf("%s %d/%d", statusIcon, ss.SuccessCount, ss.Count)
			} else {
				// Regular step
				durationStr = step.DurationStr
				memoryStr = formatMemory(step.PeakMemoryKB)
				statusStr = fmt.Sprintf("%s %s", statusIcon, step.Status)
			}

			fmt.Fprintf(w, "%-*s  %18s  %12s  %s\n",
				maxStepLen, stepID,
				durationStr,
				memoryStr,
				statusStr)
		}

		fmt.Fprintln(w, strings.Repeat("-", maxStepLen+50))

		// Print summary
		fmt.Fprintf(w, "Steps: %d completed", m.StepsComplete)
		if m.StepsFailed > 0 {
			fmt.Fprintf(w, ", %d failed", m.StepsFailed)
		}
		if m.StepsSkipped > 0 {
			fmt.Fprintf(w, ", %d skipped", m.StepsSkipped)
		}
		fmt.Fprintln(w)

		if peakMemoryKB > 0 {
			fmt.Fprintf(w, "Peak Memory: %s (%s)\n", formatMemory(peakMemoryKB), peakMemoryStep)
		}
	}

	fmt.Fprintln(w)
}

// ToMap converts WorkflowMetrics to a map suitable for JSON output.
func (m *WorkflowMetrics) ToMap() map[string]any {
	if m == nil {
		return nil
	}

	steps := make([]map[string]any, len(m.Steps))
	for i, step := range m.Steps {
		stepMap := map[string]any{
			"step_id":        step.StepID,
			"duration":       step.DurationStr,
			"duration_ns":    int64(step.Duration),
			"peak_memory_kb": step.PeakMemoryKB,
			"exit_code":      step.ExitCode,
			"status":         step.Status,
		}
		if step.ToolID != "" {
			stepMap["tool_id"] = step.ToolID
		}

		// Add scatter iteration details
		if len(step.Iterations) > 0 {
			iterations := make([]map[string]any, len(step.Iterations))
			for j, iter := range step.Iterations {
				iterations[j] = map[string]any{
					"index":          iter.Index,
					"duration":       iter.DurationStr,
					"duration_ns":    int64(iter.Duration),
					"peak_memory_kb": iter.PeakMemoryKB,
					"exit_code":      iter.ExitCode,
					"status":         iter.Status,
				}
			}
			stepMap["iterations"] = iterations
		}

		// Add scatter summary
		if step.ScatterSummary != nil {
			ss := step.ScatterSummary
			stepMap["scatter_summary"] = map[string]any{
				"count":             ss.Count,
				"duration_avg":      ss.DurationAvgStr,
				"duration_avg_ns":   int64(ss.DurationAvg),
				"duration_stddev":   ss.DurationStddevStr,
				"duration_stddev_ns": int64(ss.DurationStddev),
				"memory_avg_kb":     ss.MemoryAvgKB,
				"memory_max_kb":     ss.MemoryMaxKB,
				"success_count":     ss.SuccessCount,
				"failed_count":      ss.FailedCount,
			}
		}

		steps[i] = stepMap
	}

	result := map[string]any{
		"duration":        m.DurationStr,
		"duration_ns":     int64(m.Duration),
		"total_steps":     m.TotalSteps,
		"steps_completed": m.StepsComplete,
		"steps_failed":    m.StepsFailed,
		"steps_skipped":   m.StepsSkipped,
		"steps":           steps,
	}

	if m.WorkflowID != "" {
		result["workflow_id"] = m.WorkflowID
	}

	return result
}
