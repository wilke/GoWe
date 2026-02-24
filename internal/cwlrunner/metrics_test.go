package cwlrunner

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMetricsCollector_Disabled(t *testing.T) {
	mc := NewMetricsCollector(false)
	if mc.Enabled() {
		t.Error("expected disabled collector")
	}

	// All operations should be no-ops
	mc.SetWorkflowID("test")
	mc.SetTotalSteps(5)
	mc.RecordStep(StepMetrics{StepID: "step1", Status: "success"})

	metrics := mc.Finalize()
	if metrics != nil {
		t.Error("expected nil metrics from disabled collector")
	}
}

func TestMetricsCollector_Enabled(t *testing.T) {
	mc := NewMetricsCollector(true)
	if !mc.Enabled() {
		t.Error("expected enabled collector")
	}

	mc.SetWorkflowID("test-workflow")
	mc.SetTotalSteps(3)

	// Record a few steps
	mc.RecordStep(StepMetrics{
		StepID:       "step1",
		ToolID:       "tool1",
		StartTime:    time.Now(),
		Duration:     1 * time.Second,
		ExitCode:     0,
		PeakMemoryKB: 1024,
		Status:       "success",
	})
	mc.RecordStep(StepMetrics{
		StepID: "step2",
		ToolID: "tool2",
		Status: "skipped",
	})
	mc.RecordStep(StepMetrics{
		StepID:   "step3",
		ToolID:   "tool3",
		Status:   "failed",
		ExitCode: 1,
	})

	metrics := mc.Finalize()
	if metrics == nil {
		t.Fatal("expected non-nil metrics")
	}

	if metrics.WorkflowID != "test-workflow" {
		t.Errorf("expected workflow ID 'test-workflow', got %q", metrics.WorkflowID)
	}
	if metrics.TotalSteps != 3 {
		t.Errorf("expected 3 total steps, got %d", metrics.TotalSteps)
	}
	if metrics.StepsComplete != 1 {
		t.Errorf("expected 1 completed step, got %d", metrics.StepsComplete)
	}
	if metrics.StepsFailed != 1 {
		t.Errorf("expected 1 failed step, got %d", metrics.StepsFailed)
	}
	if metrics.StepsSkipped != 1 {
		t.Errorf("expected 1 skipped step, got %d", metrics.StepsSkipped)
	}
	if len(metrics.Steps) != 3 {
		t.Errorf("expected 3 step metrics, got %d", len(metrics.Steps))
	}
}

func TestMetricsCollector_NilSafe(t *testing.T) {
	var mc *MetricsCollector

	// All operations should be safe on nil collector
	mc.SetWorkflowID("test")
	mc.SetTotalSteps(5)
	mc.RecordStep(StepMetrics{StepID: "step1", Status: "success"})

	if mc.Enabled() {
		t.Error("expected disabled for nil collector")
	}
	if metrics := mc.Finalize(); metrics != nil {
		t.Error("expected nil metrics from nil collector")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{500 * time.Millisecond, "500ms"},
		{1 * time.Second, "1.0s"},
		{30 * time.Second, "30.0s"},
		{1 * time.Minute, "1m 00s"},
		{90 * time.Second, "1m 30s"},
		{1 * time.Hour, "1h 00m 00s"},
		{90 * time.Minute, "1h 30m 00s"},
	}

	for _, tc := range tests {
		got := formatDuration(tc.duration)
		if got != tc.expected {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.duration, got, tc.expected)
		}
	}
}

func TestFormatMemory(t *testing.T) {
	tests := []struct {
		kb       int64
		expected string
	}{
		{0, "-"},
		{512, "512 KB"},
		{1024, "1.0 MB"},
		{1536, "1.5 MB"},
		{1048576, "1.00 GB"},
	}

	for _, tc := range tests {
		got := formatMemory(tc.kb)
		if got != tc.expected {
			t.Errorf("formatMemory(%d) = %q, want %q", tc.kb, got, tc.expected)
		}
	}
}

func TestPrintMetricsSummary(t *testing.T) {
	metrics := &WorkflowMetrics{
		WorkflowID:    "test-workflow",
		DurationStr:   "1m 30s",
		TotalSteps:    3,
		StepsComplete: 2,
		StepsFailed:   1,
		Steps: []StepMetrics{
			{
				StepID:       "step1",
				DurationStr:  "30s",
				PeakMemoryKB: 1048576,
				Status:       "success",
			},
			{
				StepID:       "step2",
				DurationStr:  "45s",
				PeakMemoryKB: 524288,
				Status:       "success",
			},
			{
				StepID:      "step3",
				DurationStr: "15s",
				Status:      "failed",
			},
		},
	}

	var buf bytes.Buffer
	PrintMetricsSummary(&buf, metrics)

	output := buf.String()

	// Check for expected content
	if !strings.Contains(output, "Workflow Execution Summary") {
		t.Error("missing summary header")
	}
	if !strings.Contains(output, "test-workflow") {
		t.Error("missing workflow ID")
	}
	if !strings.Contains(output, "1m 30s") {
		t.Error("missing total duration")
	}
	if !strings.Contains(output, "step1") {
		t.Error("missing step1")
	}
	if !strings.Contains(output, "step2") {
		t.Error("missing step2")
	}
	if !strings.Contains(output, "step3") {
		t.Error("missing step3")
	}
	if !strings.Contains(output, "2 completed") {
		t.Error("missing completed count")
	}
	if !strings.Contains(output, "1 failed") {
		t.Error("missing failed count")
	}
	if !strings.Contains(output, "Peak Memory") {
		t.Error("missing peak memory")
	}
}

func TestWorkflowMetrics_ToMap(t *testing.T) {
	metrics := &WorkflowMetrics{
		WorkflowID:    "test-workflow",
		Duration:      90 * time.Second,
		DurationStr:   "1m 30s",
		TotalSteps:    2,
		StepsComplete: 2,
		Steps: []StepMetrics{
			{
				StepID:       "step1",
				ToolID:       "tool1",
				Duration:     30 * time.Second,
				DurationStr:  "30.0s",
				PeakMemoryKB: 1024,
				ExitCode:     0,
				Status:       "success",
			},
			{
				StepID:      "step2",
				Duration:    60 * time.Second,
				DurationStr: "1m 00s",
				Iterations:  4,
				Status:      "success",
			},
		},
	}

	m := metrics.ToMap()
	if m == nil {
		t.Fatal("expected non-nil map")
	}

	if m["workflow_id"] != "test-workflow" {
		t.Errorf("expected workflow_id 'test-workflow', got %v", m["workflow_id"])
	}
	if m["duration"] != "1m 30s" {
		t.Errorf("expected duration '1m 30s', got %v", m["duration"])
	}
	if m["steps_completed"] != 2 {
		t.Errorf("expected steps_completed 2, got %v", m["steps_completed"])
	}

	steps, ok := m["steps"].([]map[string]any)
	if !ok {
		t.Fatalf("expected steps to be []map[string]any, got %T", m["steps"])
	}
	if len(steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(steps))
	}

	// Check first step has tool_id
	if steps[0]["tool_id"] != "tool1" {
		t.Errorf("expected step[0].tool_id 'tool1', got %v", steps[0]["tool_id"])
	}
	// Check second step has iterations
	if steps[1]["iterations"] != 4 {
		t.Errorf("expected step[1].iterations 4, got %v", steps[1]["iterations"])
	}
}

func TestGetResourceUsage(t *testing.T) {
	// Test with nil ProcessState
	result := getResourceUsage(nil)
	if result != 0 {
		t.Errorf("expected 0 for nil ProcessState, got %d", result)
	}
}
