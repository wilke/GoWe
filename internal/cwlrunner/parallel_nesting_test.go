package cwlrunner

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// These tests exercise #183 (nested parallelism) and #50 (a global cap on
// concurrent tool executions, plus the new --cores budget). They use a
// `sleep <duration>` CommandLineTool with --no-container local execution so
// wall-clock time is a reliable, cheap proxy for "did these tool executions
// actually run concurrently". Margins are intentionally generous (CI boxes
// are noisy): each timing assertion leaves comfortable headroom rather than
// pinning to the exact expected duration.

const nestingSleepDuration = "0.3"

func newNestingLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// writeNestingFixture writes the common `sleep.cwl` tool used by all tests in
// this file: it takes a single string `duration` input and sleeps that long.
func writeSleepTool(t *testing.T, dir string) {
	t.Helper()
	content := `
cwlVersion: v1.2
class: CommandLineTool
baseCommand: sleep
inputs:
  duration:
    type: string
    inputBinding:
      position: 1
outputs: {}
`
	if err := os.WriteFile(filepath.Join(dir, "sleep.cwl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeSleepToolWithCores writes a sleep.cwl variant that declares a
// ResourceRequirement.coresMin, used by the --cores budget test.
func writeSleepToolWithCores(t *testing.T, dir string, coresMin int) {
	t.Helper()
	content := `
cwlVersion: v1.2
class: CommandLineTool
requirements:
  ResourceRequirement:
    coresMin: ` + strconv.Itoa(coresMin) + `
baseCommand: sleep
inputs:
  duration:
    type: string
    inputBinding:
      position: 1
outputs: {}
`
	if err := os.WriteFile(filepath.Join(dir, "sleep.cwl"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeJSONDurations writes a job.yml with a `durations` array of n copies of
// nestingSleepDuration.
func writeDurationsJob(t *testing.T, dir string, n int) {
	t.Helper()
	content := "durations:\n"
	for i := 0; i < n; i++ {
		content += "  - \"" + nestingSleepDuration + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "job.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// runNesting executes cwlPath/job.yml with the given parallel config and
// returns wall-clock duration. Fails the test on error.
func runNesting(t *testing.T, tmpDir, cwlFile string, cfg ParallelConfig) time.Duration {
	t.Helper()
	logger := newNestingLogger()
	runner := NewRunner(logger)
	runner.NoContainer = true
	runner.Parallel = cfg
	runner.OutDir = filepath.Join(tmpDir, "output-"+t.Name())

	ctx := context.Background()
	start := time.Now()
	if err := runner.Execute(ctx, filepath.Join(tmpDir, cwlFile), filepath.Join(tmpDir, "job.yml"), &discardWriter{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return time.Since(start)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// --- (a) scatter-in-subworkflow: a non-scattered "outer" step whose run is a
// sub-workflow that itself scatters a sleep tool over `durations`. ---

func writeScatterInSubworkflowFixtures(t *testing.T, tmpDir string, n int) {
	t.Helper()
	writeSleepTool(t, tmpDir)

	subContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  durations:
    type: string[]
outputs: {}
steps:
  scatter_step:
    run: sleep.cwl
    scatter: duration
    in:
      duration: durations
    out: []
`
	if err := os.WriteFile(filepath.Join(tmpDir, "sub.cwl"), []byte(subContent), 0644); err != nil {
		t.Fatal(err)
	}

	mainContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  durations:
    type: string[]
outputs: {}
steps:
  outer:
    run: sub.cwl
    in:
      durations: durations
    out: []
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.cwl"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}

	writeDurationsJob(t, tmpDir, n)
}

func TestExecuteSubWorkflow_ScatterInSubworkflow_ParallelFasterThanSerial(t *testing.T) {
	const n = 4
	serialSum := time.Duration(n) * 300 * time.Millisecond

	tmpDir := t.TempDir()
	writeScatterInSubworkflowFixtures(t, tmpDir, n)

	elapsed := runNesting(t, tmpDir, "main.cwl", ParallelConfig{
		Enabled:    true,
		MaxWorkers: 4,
		FailFast:   true,
	})

	t.Logf("scatter-in-subworkflow parallel elapsed=%s serialSum=%s", elapsed, serialSum)

	// Proves the fix for #183 item 2/3: previously executeSubWorkflow never
	// consulted r.Parallel, so the inner scatter always ran serially
	// regardless of --parallel (elapsed ~= serialSum). Generous margin for
	// CI noise: parallel must land well under the serial sum.
	if elapsed >= serialSum*6/10 {
		t.Errorf("scatter-in-subworkflow did not run in parallel: elapsed=%s, want < 60%% of serialSum=%s", elapsed, serialSum)
	}
}

// --- (b) scatter-over-subworkflow: a top-level step scatters over
// `durations`, and each iteration invokes a sub-workflow containing one
// sleep step. ---

func writeScatterOverSubworkflowFixtures(t *testing.T, tmpDir string, n int) {
	t.Helper()
	writeSleepTool(t, tmpDir)

	subContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  duration:
    type: string
outputs: {}
steps:
  sleep_step:
    run: sleep.cwl
    in:
      duration: duration
    out: []
`
	if err := os.WriteFile(filepath.Join(tmpDir, "sub.cwl"), []byte(subContent), 0644); err != nil {
		t.Fatal(err)
	}

	mainContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  durations:
    type: string[]
outputs: {}
steps:
  scatter_sub:
    run: sub.cwl
    scatter: duration
    in:
      duration: durations
    out: []
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.cwl"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}

	writeDurationsJob(t, tmpDir, n)
}

func TestExecuteScatterSubWorkflow_ParallelFasterThanSerial(t *testing.T) {
	const n = 4
	serialSum := time.Duration(n) * 300 * time.Millisecond

	tmpDir := t.TempDir()
	writeScatterOverSubworkflowFixtures(t, tmpDir, n)

	elapsed := runNesting(t, tmpDir, "main.cwl", ParallelConfig{
		Enabled:    true,
		MaxWorkers: 4,
		FailFast:   true,
	})

	t.Logf("scatter-over-subworkflow parallel elapsed=%s serialSum=%s", elapsed, serialSum)

	// Proves the fix for #183 item 4: executeScatterSubWorkflow was an
	// explicit serial for loop; iterations must now run concurrently.
	if elapsed >= serialSum*6/10 {
		t.Errorf("scatter-over-subworkflow did not run in parallel: elapsed=%s, want < 60%% of serialSum=%s", elapsed, serialSum)
	}
}

// --- (c) -j 1 forces serial timing even nested: proves the global cap binds
// across nesting levels rather than each level getting its own budget. Uses
// the scatter-over-subworkflow shape, which spins up multiple concurrent
// parallelExecutor instances (one per scatter iteration's child
// executeSubWorkflow call) -- the strongest test that a single Runner-level
// semaphore, not a per-executor one, is what's shared. ---

func TestExecuteScatterSubWorkflow_JobsOne_ForcesSerialTimingEvenNested(t *testing.T) {
	const n = 4
	serialSum := time.Duration(n) * 300 * time.Millisecond

	tmpDir := t.TempDir()
	writeScatterOverSubworkflowFixtures(t, tmpDir, n)

	elapsed := runNesting(t, tmpDir, "main.cwl", ParallelConfig{
		Enabled:    true,
		MaxWorkers: 1,
		FailFast:   true,
	})

	t.Logf("scatter-over-subworkflow -j1 elapsed=%s serialSum=%s", elapsed, serialSum)

	// With a global cap of 1 concurrent tool execution, wall time must be
	// close to the full serial sum even though scatter-over-subworkflow
	// iterations are structurally concurrent (goroutines). Generous lower
	// bound for CI noise.
	if elapsed < serialSum*9/10 {
		t.Errorf("expected -j 1 to force serial timing even nested: elapsed=%s, want >= 90%% of serialSum=%s (global cap did not bind across nesting)", elapsed, serialSum)
	}
}

// --- (d) --cores budget admits floor(N/w) concurrently. ---

func TestExecuteScatter_CoresBudget_AdmitsFloorNOverW(t *testing.T) {
	const n = 4
	const coresPerTool = 2
	const budget = 4
	const waves = 2 // floor(budget / coresPerTool)
	sleepDur := 300 * time.Millisecond
	serialSum := time.Duration(n) * sleepDur
	expectedWavesDuration := time.Duration(waves) * sleepDur

	tmpDir := t.TempDir()
	writeSleepToolWithCores(t, tmpDir, coresPerTool)

	mainContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  durations:
    type: string[]
outputs: {}
steps:
  scatter_step:
    run: sleep.cwl
    scatter: duration
    in:
      duration: durations
    out: []
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.cwl"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}
	writeDurationsJob(t, tmpDir, n)

	elapsed := runNesting(t, tmpDir, "main.cwl", ParallelConfig{
		Enabled:     true,
		MaxWorkers:  4, // not the binding constraint here -- cores is
		CoresBudget: budget,
		FailFast:    true,
	})

	t.Logf("cores-budget elapsed=%s expectedWaves(%d)Duration=%s serialSum=%s", elapsed, waves, expectedWavesDuration, serialSum)

	// Lower bound is the discriminating assertion: it proves the budget
	// actually gated admission down to 2 waves rather than running all 4
	// concurrently (which would land near one sleepDur).
	if elapsed < expectedWavesDuration*9/10 {
		t.Errorf("--cores budget did not gate admission: elapsed=%s, want >= 90%% of %d waves * sleep = %s", elapsed, waves, expectedWavesDuration)
	}
	// Upper bound proves it wasn't fully serial either.
	if elapsed >= serialSum*8/10 {
		t.Errorf("--cores budget over-serialized: elapsed=%s, want < 80%% of serialSum=%s", elapsed, serialSum)
	}
}

// --- Regression test for the ExpressionTool-scatter-in-subworkflow hazard
// caught during review: pe.executeStep's ExpressionTool branch previously had
// no scatter check, so a scattered ExpressionTool inside a (now parallel)
// sub-workflow would execute once with unexpanded array inputs instead of
// once per combination. ---

func TestExecuteSubWorkflow_ScatteredExpressionTool_Parallel(t *testing.T) {
	tmpDir := t.TempDir()

	subContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  values:
    type: int[]
outputs:
  doubled:
    type: int[]
    outputSource: double_step/doubled
steps:
  double_step:
    run:
      class: ExpressionTool
      inputs:
        value:
          type: int
      outputs:
        doubled:
          type: int
      expression: "${return {'doubled': inputs.value * 2};}"
    scatter: value
    in:
      value: values
    out: [doubled]
`
	if err := os.WriteFile(filepath.Join(tmpDir, "sub.cwl"), []byte(subContent), 0644); err != nil {
		t.Fatal(err)
	}

	mainContent := `
cwlVersion: v1.2
class: Workflow
inputs:
  values:
    type: int[]
outputs:
  doubled:
    type: int[]
    outputSource: outer/doubled
steps:
  outer:
    run: sub.cwl
    in:
      values: values
    out: [doubled]
`
	if err := os.WriteFile(filepath.Join(tmpDir, "main.cwl"), []byte(mainContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "job.yml"), []byte("values: [1, 2, 3]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := newNestingLogger()
	runner := NewRunner(logger)
	runner.NoContainer = true
	runner.Parallel.Enabled = true
	runner.Parallel.MaxWorkers = 4
	runner.OutDir = filepath.Join(tmpDir, "output")

	var buf discardWriter
	ctx := context.Background()
	if err := runner.Execute(ctx, filepath.Join(tmpDir, "main.cwl"), filepath.Join(tmpDir, "job.yml"), &buf); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}
