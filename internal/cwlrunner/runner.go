// Package cwlrunner provides a CWL v1.2 runner implementation.
package cwlrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/cwloutput"
	"github.com/me/gowe/internal/exprtool"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/iwdr"
	"github.com/me/gowe/internal/loadcontents"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/cwl"
	"gopkg.in/yaml.v3"
)

// Runner executes CWL tools and workflows.
type Runner struct {
	logger *slog.Logger
	parser *parser.Parser

	// Configuration options.
	OutDir           string
	NoContainer      bool
	ForceDocker      bool
	ContainerRuntime string // "docker", "apptainer", or "" (auto-detect)
	OutputFormat     string // "json" or "yaml"
	ProcessID        string // specific process ID to run from $graph document

	// Parallel execution configuration.
	Parallel ParallelConfig

	// Metrics collection.
	CollectMetrics bool              // Enable metrics collection
	metrics        *MetricsCollector // Internal metrics collector

	// Internal state.
	cwlDir          string            // directory of CWL file, for resolving relative paths in defaults
	stepCount       int               // counter for unique step directories
	stepMu          sync.Mutex        // protects stepCount for parallel execution
	namespaces      map[string]string // namespace prefix -> URI mappings
	jobRequirements []any             // cwl:requirements from job file
}

// NewRunner creates a new CWL runner.
func NewRunner(logger *slog.Logger) *Runner {
	return &Runner{
		logger:       logger,
		parser:       parser.New(logger),
		OutDir:       "./cwl-output",
		OutputFormat: "json",
		Parallel:     DefaultParallelConfig(),
	}
}

// LoadDocument loads and parses a CWL document from a file.
func (r *Runner) LoadDocument(cwlPath string) (*cwl.GraphDocument, error) {
	data, err := os.ReadFile(cwlPath)
	if err != nil {
		return nil, fmt.Errorf("read CWL file: %w", err)
	}

	// Store CWL directory for resolving relative paths in defaults and imports.
	r.cwlDir = filepath.Dir(cwlPath)
	if r.cwlDir == "" {
		r.cwlDir = "."
	}

	// Use ParseGraphWithBase to resolve $import directives.
	graph, err := r.parser.ParseGraphWithBase(data, r.cwlDir)
	if err != nil {
		return nil, fmt.Errorf("parse CWL: %w", err)
	}

	return graph, nil
}

// LoadInputs loads and parses a job input file.
// Also extracts cwl:requirements from the job file and stores them in the runner.
func (r *Runner) LoadInputs(jobPath string) (map[string]any, error) {
	if jobPath == "" {
		r.jobRequirements = nil
		return make(map[string]any), nil
	}

	data, err := os.ReadFile(jobPath)
	if err != nil {
		return nil, fmt.Errorf("read job file: %w", err)
	}

	var inputs map[string]any
	if err := yaml.Unmarshal(data, &inputs); err != nil {
		return nil, fmt.Errorf("parse job YAML: %w", err)
	}

	// Extract cwl:requirements from job file.
	if reqs, ok := inputs["cwl:requirements"]; ok {
		if reqsList, ok := reqs.([]any); ok {
			r.jobRequirements = reqsList
		}
		delete(inputs, "cwl:requirements")
	} else {
		r.jobRequirements = nil
	}

	return inputs, nil
}

// Validate validates a CWL document.
func (r *Runner) Validate(ctx context.Context, cwlPath string) error {
	graph, err := r.LoadDocument(cwlPath)
	if err != nil {
		return err
	}

	v := parser.NewValidator(r.logger)
	if err := v.Validate(graph); err != nil {
		return err
	}

	return nil
}

// PrintDAG prints the workflow DAG as JSON.
func (r *Runner) PrintDAG(ctx context.Context, cwlPath, jobPath string, w io.Writer) error {
	graph, err := r.LoadDocument(cwlPath)
	if err != nil {
		return err
	}

	inputs, err := r.LoadInputs(jobPath)
	if err != nil {
		return err
	}

	dag := buildDAGOutput(graph, inputs)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(dag)
}

// PrintCommandLine prints the command line without executing.
func (r *Runner) PrintCommandLine(ctx context.Context, cwlPath, jobPath string, w io.Writer) error {
	graph, err := r.LoadDocument(cwlPath)
	if err != nil {
		return err
	}

	inputs, err := r.LoadInputs(jobPath)
	if err != nil {
		return err
	}

	// Resolve input file paths.
	jobDir := filepath.Dir(jobPath)
	if jobPath == "" {
		jobDir = "."
	}
	resolvedInputs := resolveInputPaths(inputs, jobDir)

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph, r.cwlDir)

	// Build command line for each tool.
	for toolID, tool := range graph.Tools {
		// Merge tool defaults with resolved inputs.
		mergedInputs, err := mergeToolDefaults(tool, resolvedInputs, r.cwlDir)
		if err != nil {
			return fmt.Errorf("process inputs for %s: %w", toolID, err)
		}

		// Build runtime context from tool requirements (with inputs for dynamic resource expressions).
		runtime := buildRuntimeContextWithInputs(tool, r.OutDir, mergedInputs, expressionLib)

		builder := cmdline.NewBuilder(expressionLib)
		result, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return fmt.Errorf("build command for %s: %w", toolID, err)
		}

		fmt.Fprintf(w, "# Tool: %s\n", toolID)
		for _, arg := range result.Command {
			fmt.Fprintf(w, "%s ", arg)
		}
		fmt.Fprintln(w)

		if result.Stdin != "" {
			fmt.Fprintf(w, "# stdin: %s\n", result.Stdin)
		}
		if result.Stdout != "" {
			fmt.Fprintf(w, "# stdout: %s\n", result.Stdout)
		}
		if result.Stderr != "" {
			fmt.Fprintf(w, "# stderr: %s\n", result.Stderr)
		}
		fmt.Fprintln(w)
	}

	return nil
}

// Execute runs a CWL tool or workflow.
func (r *Runner) Execute(ctx context.Context, cwlPath, jobPath string, w io.Writer) error {
	graph, err := r.LoadDocument(cwlPath)
	if err != nil {
		return err
	}

	// Store namespaces for format resolution.
	r.namespaces = graph.Namespaces

	// Validate first.
	v := parser.NewValidator(r.logger)
	if err := v.Validate(graph); err != nil {
		return err
	}

	inputs, err := r.LoadInputs(jobPath)
	if err != nil {
		return err
	}

	// Resolve input file paths relative to job file.
	jobDir := filepath.Dir(jobPath)
	if jobPath == "" {
		jobDir = "."
	}
	resolvedInputs := resolveInputPaths(inputs, jobDir)

	// Create output directory.
	if err := os.MkdirAll(r.OutDir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Initialize metrics collector if enabled.
	r.metrics = NewMetricsCollector(r.CollectMetrics)

	// If ProcessID is specified, select that specific process.
	if r.ProcessID != "" {
		// Normalize processID for comparison (strip leading #)
		processID := r.ProcessID
		processIDWithHash := "#" + r.ProcessID

		// Check if it's a tool.
		if tool, ok := graph.Tools[processID]; ok {
			r.metrics.SetWorkflowID(tool.ID)
			r.metrics.SetTotalSteps(1)
			outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
			if err != nil {
				r.finalizeAndPrintMetrics(w)
				return err
			}
			return r.writeOutputsWithMetrics(outputs, w)
		}
		if tool, ok := graph.Tools[processIDWithHash]; ok {
			r.metrics.SetWorkflowID(tool.ID)
			r.metrics.SetTotalSteps(1)
			outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
			if err != nil {
				r.finalizeAndPrintMetrics(w)
				return err
			}
			return r.writeOutputsWithMetrics(outputs, w)
		}
		// Check if it matches the workflow ID (with or without # prefix).
		if graph.Workflow != nil {
			wfID := graph.Workflow.ID
			if wfID == processID || wfID == processIDWithHash || wfID == "#"+processID {
				return r.executeWorkflow(ctx, graph, resolvedInputs, w)
			}
		}
		return fmt.Errorf("process %q not found in document", r.ProcessID)
	}

	// Execute based on document type.
	totalTools := len(graph.Tools) + len(graph.ExpressionTools)
	if graph.OriginalClass == "Workflow" || totalTools > 1 {
		return r.executeWorkflow(ctx, graph, resolvedInputs, w)
	}

	// Single CommandLineTool execution.
	for _, tool := range graph.Tools {
		r.metrics.SetWorkflowID(tool.ID)
		r.metrics.SetTotalSteps(1)
		outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
		if err != nil {
			r.finalizeAndPrintMetrics(w)
			return err
		}
		return r.writeOutputsWithMetrics(outputs, w)
	}

	// Single ExpressionTool execution.
	for _, exprTool := range graph.ExpressionTools {
		r.metrics.SetWorkflowID(exprTool.ID)
		r.metrics.SetTotalSteps(1)
		outputs, err := r.executeExpressionTool(exprTool, resolvedInputs, graph)
		if err != nil {
			r.finalizeAndPrintMetrics(w)
			return err
		}
		return r.writeOutputsWithMetrics(outputs, w)
	}

	return fmt.Errorf("no tools found in document")
}

// finalizeAndPrintMetrics finalizes metrics and prints summary to stderr.
func (r *Runner) finalizeAndPrintMetrics(w io.Writer) {
	if r.metrics == nil || !r.metrics.Enabled() {
		return
	}
	metrics := r.metrics.Finalize()
	PrintMetricsSummary(os.Stderr, metrics)
}

// writeOutputsWithMetrics writes outputs and includes metrics if enabled.
func (r *Runner) writeOutputsWithMetrics(outputs map[string]any, w io.Writer) error {
	// Finalize metrics first
	var metricsMap map[string]any
	if r.metrics != nil && r.metrics.Enabled() {
		metrics := r.metrics.Finalize()
		metricsMap = metrics.ToMap()
		// Print summary to stderr
		PrintMetricsSummary(os.Stderr, metrics)
	}

	// Write outputs with optional metrics
	return r.writeOutputsInternal(outputs, metricsMap, w)
}

// executeTool executes a single CommandLineTool.
// If resolveSecondary is true, secondary files will be resolved from tool definitions.
// For workflow steps, secondary files should already be resolved from workflow inputs.
func (r *Runner) executeTool(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, inputs map[string]any, resolveSecondary bool) (map[string]any, error) {
	return r.executeToolWithStepID(ctx, graph, tool, inputs, resolveSecondary, "")
}

// executeToolWithStepID executes a single CommandLineTool with an optional step ID for metrics.
func (r *Runner) executeToolWithStepID(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, inputs map[string]any, resolveSecondary bool, stepID string) (map[string]any, error) {
	r.logger.Info("executing tool", "id", tool.ID)

	// Merge workflow requirements into tool (workflow requirements override tool hints).
	mergeWorkflowRequirements(tool, graph.Workflow)

	// Resolve secondaryFiles for tool inputs if requested (direct tool execution).
	resolvedInputs := inputs
	if resolveSecondary {
		resolvedInputs = secondaryfiles.ResolveForTool(tool, inputs, r.cwlDir)
	}

	// Merge tool input defaults with resolved inputs.
	mergedInputs, err := mergeToolDefaults(tool, resolvedInputs, r.cwlDir)
	if err != nil {
		return nil, fmt.Errorf("process inputs: %w", err)
	}

	// Validate inputs against tool schema.
	if err := validate.ToolInputs(tool, mergedInputs); err != nil {
		return nil, err
	}

	// Validate file formats.
	if err := validate.ValidateFileFormat(tool, mergedInputs, r.namespaces); err != nil {
		return nil, err
	}

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph, r.cwlDir)

	// Determine container runtime.
	// Priority: NoContainer (forces local) > ContainerRuntime (explicit) > ForceDocker > auto-detect.
	containerRuntime := r.ContainerRuntime
	if r.NoContainer {
		containerRuntime = ""
	} else if containerRuntime == "" {
		if r.ForceDocker {
			containerRuntime = "docker"
		} else if hasDockerRequirement(tool, graph.Workflow) {
			// Auto-detect: default to "docker" when DockerRequirement is present.
			containerRuntime = "docker"
		}
	}

	// Get the work directory for this execution (increments stepCount).
	// Use mutex for thread-safety in parallel execution.
	r.stepMu.Lock()
	r.stepCount++
	stepNum := r.stepCount
	r.stepMu.Unlock()
	workDir := filepath.Join(r.OutDir, fmt.Sprintf("work_%d", stepNum))
	// Make workDir absolute for use in runtime.outdir expressions.
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
	}

	// Ensure work directory exists for staging.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	// Populate directory listings for inputs with loadListing.
	populateDirectoryListings(tool, mergedInputs)

	// Stage files from InitialWorkDirRequirement.
	// For container execution, copy files instead of symlinking (symlinks point to host paths).
	// Check for InplaceUpdateRequirement - if enabled, writable files should be symlinked
	// so modifications affect the original (required for workflows that depend on side effects).
	useContainer := containerRuntime == "docker" || containerRuntime == "apptainer"
	inplaceUpdate := hasInplaceUpdateRequirement(tool)
	iwdResult, err := iwdr.Stage(tool, mergedInputs, workDir, iwdr.StageOptions{
		CopyForContainer: useContainer,
		CWLDir:           r.cwlDir,
		ExpressionLib:    expressionLib,
		InplaceUpdate:    inplaceUpdate,
	})
	if err != nil {
		return nil, fmt.Errorf("stage InitialWorkDirRequirement: %w", err)
	}
	var containerMounts []iwdr.ContainerMount
	if iwdResult != nil {
		containerMounts = iwdResult.ContainerMounts
		iwdr.UpdateInputPaths(mergedInputs, workDir, iwdResult.StagedPaths)
	}

	// Stage files with renamed basenames (e.g., from ExpressionTool modifications).
	// When a File has basename != filepath.Base(path), we need to symlink it with
	// the new basename so that $(inputs.x.path) returns the correct path.
	if err := stageRenamedInputs(mergedInputs, workDir); err != nil {
		return nil, fmt.Errorf("stage renamed inputs: %w", err)
	}

	// Apply ToolTimeLimit if specified.
	// Wrap context with timeout to enforce the time limit.
	timeLimit := getToolTimeLimit(tool, mergedInputs)
	if timeLimit < 0 {
		return nil, fmt.Errorf("invalid ToolTimeLimit: timelimit must be non-negative, got %d", timeLimit)
	}
	if timeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeLimit)*time.Second)
		defer cancel()
	}

	// Build command line with runtime context appropriate for the execution environment.
	// For containers, use container paths; for local execution, use host paths.
	builder := cmdline.NewBuilder(expressionLib)

	var result *ExecutionResult
	var execErr error
	switch containerRuntime {
	case "docker":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		dockerOutputDir := getDockerOutputDirectory(tool)
		// Build runtime context with container paths.
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := buildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		r.logger.Debug("built command", "cmd", cmdResult.Command)
		result, execErr = r.executeInDockerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir, containerMounts, dockerOutputDir)
	case "apptainer":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Apptainer execution requested but no docker image specified")
		}
		dockerOutputDir := getDockerOutputDirectory(tool)
		// Build runtime context with container paths.
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := buildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		r.logger.Debug("built command", "cmd", cmdResult.Command)
		result, execErr = r.executeInApptainerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir, containerMounts, dockerOutputDir)
	default:
		// Build runtime context using actual work directory for local execution.
		runtime := buildRuntimeContextWithInputs(tool, workDir, mergedInputs, expressionLib)
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		r.logger.Debug("built command", "cmd", cmdResult.Command)
		result, execErr = r.executeLocalWithWorkDir(ctx, tool, cmdResult, mergedInputs, workDir)
	}

	if execErr != nil {
		// Record failed step metrics if enabled
		if r.metrics != nil && r.metrics.Enabled() {
			metricsStepID := stepID
			if metricsStepID == "" {
				metricsStepID = tool.ID
			}
			r.metrics.RecordStep(StepMetrics{
				StepID:   metricsStepID,
				ToolID:   tool.ID,
				Status:   "failed",
				ExitCode: -1,
			})
		}
		return nil, execErr
	}

	// Record successful step metrics if enabled
	if r.metrics != nil && r.metrics.Enabled() {
		metricsStepID := stepID
		if metricsStepID == "" {
			metricsStepID = tool.ID
		}
		r.metrics.RecordStep(StepMetrics{
			StepID:       metricsStepID,
			ToolID:       tool.ID,
			StartTime:    result.StartTime,
			Duration:     result.Duration,
			ExitCode:     result.ExitCode,
			PeakMemoryKB: result.PeakMemoryKB,
			Status:       "success",
		})
	}

	return result.Outputs, nil
}

// executeScatterWithMetrics executes a scatter step and records per-iteration metrics.
func (r *Runner) executeScatterWithMetrics(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any, stepID string,
	evaluator *cwlexpr.Evaluator) (map[string]any, error) {

	startTime := time.Now()

	// Execute the scatter with iteration tracking
	outputs, iterMetrics, err := r.executeScatterWithIterationMetrics(ctx, graph, tool, step, inputs, evaluator)

	duration := time.Since(startTime)

	// Record metrics for the scatter step
	if r.metrics != nil && r.metrics.Enabled() {
		status := "success"
		if err != nil {
			status = "failed"
		}

		stepMetrics := StepMetrics{
			StepID:     stepID,
			ToolID:     tool.ID,
			StartTime:  startTime,
			Duration:   duration,
			Status:     status,
			Iterations: iterMetrics,
		}

		// Compute scatter summary from iteration metrics
		if len(iterMetrics) > 0 {
			stepMetrics.ScatterSummary = ComputeScatterSummary(iterMetrics)
		}

		r.metrics.RecordStep(stepMetrics)
	}

	return outputs, err
}

// executeScatterWithIterationMetrics executes scatter and returns per-iteration metrics.
func (r *Runner) executeScatterWithIterationMetrics(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, step cwl.Step, inputs map[string]any,
	evaluator *cwlexpr.Evaluator) (map[string]any, []IterationMetrics, error) {

	if len(step.Scatter) == 0 {
		return nil, nil, fmt.Errorf("no scatter inputs specified")
	}

	// Get evaluator if provided.
	var eval *cwlexpr.Evaluator
	if evaluator != nil {
		eval = evaluator
	}

	// Check 'when' condition early (before scatter validation).
	if step.When != "" && eval != nil && !whenReferencesScatterVars(step.When, step.Scatter) {
		evalCtx := cwlexpr.NewContext(inputs)
		shouldRun, err := eval.EvaluateBool(step.When, evalCtx)
		if err != nil {
			r.logger.Debug("when condition pre-check failed, will evaluate per-iteration", "error", err)
		} else if !shouldRun {
			r.logger.Info("skipping scatter step (when condition false)", "step", step.Run)
			outputs := make(map[string]any)
			for _, outID := range step.Out {
				outputs[outID] = nil
			}
			return outputs, nil, nil
		}
	}

	// Determine scatter method
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Generate input combinations
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = dotProduct(inputs, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = nestedCrossProduct(inputs, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = flatCrossProduct(inputs, step.Scatter, scatterArrays)
	default:
		return nil, nil, fmt.Errorf("unknown scatter method: %s", method)
	}

	// Evaluate valueFrom expressions per scatter iteration
	if eval != nil && hasValueFrom(step) {
		for _, combo := range combinations {
			if err := evaluateValueFrom(step, combo, eval); err != nil {
				return nil, nil, fmt.Errorf("scatter valueFrom: %w", err)
			}
		}
	}

	// Execute tool for each combination, collecting iteration metrics
	var results []map[string]any
	var iterMetrics []IterationMetrics
	for i, combo := range combinations {
		r.logger.Debug("scatter iteration", "index", i, "inputs", combo)

		iterStart := time.Now()
		var iterStatus string
		var iterExitCode int
		var iterMemory int64

		// Evaluate 'when' condition if present
		if step.When != "" && eval != nil {
			evalCtx := cwlexpr.NewContext(combo)
			shouldRun, err := eval.EvaluateBool(step.When, evalCtx)
			if err != nil {
				return nil, nil, fmt.Errorf("scatter iteration %d when: %w", i, err)
			}
			if !shouldRun {
				// Condition is false for this iteration
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				results = append(results, nullOutputs)
				iterMetrics = append(iterMetrics, IterationMetrics{
					Index:       i,
					Duration:    time.Since(iterStart),
					DurationStr: formatDuration(time.Since(iterStart)),
					Status:      "skipped",
				})
				continue
			}
		}

		// Execute the tool
		result, err := r.executeToolInternal(ctx, graph, tool, combo)
		if err != nil {
			return nil, nil, fmt.Errorf("scatter iteration %d: %w", i, err)
		}

		results = append(results, result.Outputs)
		iterStatus = "success"
		iterExitCode = result.ExitCode
		iterMemory = result.PeakMemoryKB

		iterMetrics = append(iterMetrics, IterationMetrics{
			Index:        i,
			Duration:     result.Duration,
			DurationStr:  formatDuration(result.Duration),
			PeakMemoryKB: iterMemory,
			ExitCode:     iterExitCode,
			Status:       iterStatus,
		})
	}

	// Merge results into output arrays
	var outputs map[string]any
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		dims := make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			dims[i] = len(scatterArrays[name])
		}
		outputs = mergeScatterOutputsNested(results, tool, dims)
	} else {
		outputs = mergeScatterOutputs(results, tool)
	}

	return outputs, iterMetrics, nil
}

// executeToolInternal executes a tool and returns ExecutionResult without recording metrics.
// This is used by scatter executors to collect per-iteration results.
func (r *Runner) executeToolInternal(ctx context.Context, graph *cwl.GraphDocument,
	tool *cwl.CommandLineTool, inputs map[string]any) (*ExecutionResult, error) {

	// Merge tool input defaults with resolved inputs.
	mergedInputs, err := mergeToolDefaults(tool, inputs, r.cwlDir)
	if err != nil {
		return nil, fmt.Errorf("process inputs: %w", err)
	}

	// Validate inputs against tool schema.
	if err := validate.ToolInputs(tool, mergedInputs); err != nil {
		return nil, err
	}

	// Validate file formats.
	if err := validate.ValidateFileFormat(tool, mergedInputs, r.namespaces); err != nil {
		return nil, err
	}

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph, r.cwlDir)

	// Determine container runtime.
	containerRuntime := r.ContainerRuntime
	if r.NoContainer {
		containerRuntime = ""
	} else if containerRuntime == "" {
		if r.ForceDocker {
			containerRuntime = "docker"
		} else if hasDockerRequirement(tool, graph.Workflow) {
			containerRuntime = "docker"
		}
	}

	// Get the work directory for this execution.
	r.stepMu.Lock()
	r.stepCount++
	stepNum := r.stepCount
	r.stepMu.Unlock()
	workDir := filepath.Join(r.OutDir, fmt.Sprintf("work_%d", stepNum))
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
	}

	// Ensure work directory exists for staging.
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}

	// Stage files from InitialWorkDirRequirement.
	// Check for InplaceUpdateRequirement - if enabled, writable files should be symlinked
	// so modifications affect the original (required for workflows that depend on side effects).
	useContainer := containerRuntime == "docker" || containerRuntime == "apptainer"
	inplaceUpdate := hasInplaceUpdateRequirement(tool)
	iwdResult, err := iwdr.Stage(tool, mergedInputs, workDir, iwdr.StageOptions{
		CopyForContainer: useContainer,
		CWLDir:           r.cwlDir,
		ExpressionLib:    expressionLib,
		InplaceUpdate:    inplaceUpdate,
	})
	if err != nil {
		return nil, fmt.Errorf("stage InitialWorkDirRequirement: %w", err)
	}
	var containerMounts []iwdr.ContainerMount
	if iwdResult != nil {
		containerMounts = iwdResult.ContainerMounts
		iwdr.UpdateInputPaths(mergedInputs, workDir, iwdResult.StagedPaths)
	}

	// Stage files with renamed basenames (e.g., from ExpressionTool modifications).
	if err := stageRenamedInputs(mergedInputs, workDir); err != nil {
		return nil, fmt.Errorf("stage renamed inputs: %w", err)
	}

	// Build command line with runtime context appropriate for the execution environment.
	builder := cmdline.NewBuilder(expressionLib)

	// Apply ToolTimeLimit if specified.
	timeLimit := getToolTimeLimit(tool, mergedInputs)
	if timeLimit < 0 {
		return nil, fmt.Errorf("invalid ToolTimeLimit: timelimit must be non-negative, got %d", timeLimit)
	}
	if timeLimit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeLimit)*time.Second)
		defer cancel()
	}

	// Execute based on container runtime
	switch containerRuntime {
	case "docker":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		dockerOutputDir := getDockerOutputDirectory(tool)
		// Build runtime context with container paths.
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := buildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		return r.executeInDockerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir, containerMounts, dockerOutputDir)
	case "apptainer":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Apptainer execution requested but no docker image specified")
		}
		dockerOutputDir := getDockerOutputDirectory(tool)
		// Build runtime context with container paths.
		containerWorkDir := "/var/spool/cwl"
		if dockerOutputDir != "" {
			containerWorkDir = dockerOutputDir
		}
		runtime := buildRuntimeContextWithInputs(tool, containerWorkDir, mergedInputs, expressionLib)
		runtime.TmpDir = "/tmp"
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		return r.executeInApptainerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir, containerMounts, dockerOutputDir)
	default:
		// Build runtime context using actual work directory for local execution.
		runtime := buildRuntimeContextWithInputs(tool, workDir, mergedInputs, expressionLib)
		cmdResult, err := builder.Build(tool, mergedInputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("build command: %w", err)
		}
		return r.executeLocalWithWorkDir(ctx, tool, cmdResult, mergedInputs, workDir)
	}
}

// executeExpressionTool executes a CWL ExpressionTool by evaluating its JavaScript expression.
func (r *Runner) executeExpressionTool(tool *cwl.ExpressionTool, inputs map[string]any, graph *cwl.GraphDocument) (map[string]any, error) {
	r.logger.Info("executing expression tool", "id", tool.ID)

	// Validate inputs against tool schema.
	if err := validate.ExpressionToolInputs(tool, inputs); err != nil {
		return nil, err
	}

	// Populate directory listings for inputs with loadListing.
	populateExpressionToolDirectoryListings(tool, inputs)

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph, r.cwlDir)

	// Execute the expression.
	outputs, err := exprtool.Execute(tool, inputs, exprtool.ExecuteOptions{
		ExpressionLib: expressionLib,
		CWLDir:        r.cwlDir,
	})
	if err != nil {
		return nil, err
	}

	// Materialize file/directory literals in outputs.
	return fileliteral.MaterializeOutputs(outputs, r.OutDir)
}

// executeSubWorkflow executes a nested workflow and returns its outputs.
func (r *Runner) executeSubWorkflow(ctx context.Context, subGraph *cwl.GraphDocument, inputs map[string]any) (map[string]any, error) {
	// Merge workflow input defaults with provided inputs.
	mergedInputs := mergeWorkflowInputDefaults(subGraph.Workflow, inputs, r.cwlDir)

	// Build execution order using DAG.
	dag, err := parser.BuildDAG(subGraph.Workflow)
	if err != nil {
		return nil, fmt.Errorf("build DAG: %w", err)
	}

	// Track outputs from completed steps.
	stepOutputs := make(map[string]map[string]any)

	// Create evaluator for expressions (valueFrom, when).
	evaluator := cwlexpr.NewEvaluator(extractExpressionLib(subGraph, r.cwlDir))

	// Execute steps in topological order.
	for _, stepID := range dag.Order {
		step := subGraph.Workflow.Steps[stepID]

		// Resolve step inputs.
		var stepEvaluator *cwlexpr.Evaluator
		if len(step.Scatter) == 0 {
			stepEvaluator = evaluator
		}
		stepInputs, err := resolveStepInputs(step, mergedInputs, stepOutputs, r.cwlDir, stepEvaluator)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", stepID, err)
		}

		// Check if this is an ExpressionTool.
		toolRef := stripHash(step.Run)
		if exprTool, ok := subGraph.ExpressionTools[toolRef]; ok {
			// Handle scatter if present.
			if len(step.Scatter) > 0 {
				outputs, err := r.executeScatterExpressionTool(ctx, subGraph, exprTool, step, stepInputs, evaluator)
				if err != nil {
					return nil, fmt.Errorf("step %s: %w", stepID, err)
				}
				stepOutputs[stepID] = outputs
				continue
			}

			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return nil, fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					continue
				}
			}

			outputs, err := r.executeExpressionTool(exprTool, stepInputs, subGraph)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
			continue
		}

		// Check if this is a nested SubWorkflow.
		if nestedGraph, ok := subGraph.SubWorkflows[toolRef]; ok {
			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return nil, fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					continue
				}
			}

			r.logger.Info("executing nested subworkflow", "step", stepID, "workflow", toolRef)
			outputs, err := r.executeSubWorkflow(ctx, nestedGraph, stepInputs)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
			continue
		}

		// Otherwise it's a CommandLineTool.
		tool := subGraph.Tools[toolRef]
		if tool == nil {
			return nil, fmt.Errorf("step %s: tool %s not found", stepID, step.Run)
		}

		// Merge step requirements into tool.
		mergeStepRequirements(tool, &step)

		// Handle scatter if present.
		if len(step.Scatter) > 0 {
			outputs, err := r.executeScatterWithMetrics(ctx, subGraph, tool, step, stepInputs, stepID, evaluator)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		} else {
			// Handle conditional execution for non-scattered steps.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return nil, fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					continue
				}
			}

			outputs, err := r.executeToolWithStepID(ctx, subGraph, tool, stepInputs, false, stepID)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		}
	}

	// Collect workflow outputs (pass inputs for passthrough workflows).
	workflowOutputs, err := collectWorkflowOutputs(subGraph.Workflow, mergedInputs, stepOutputs)
	if err != nil {
		return nil, fmt.Errorf("collect outputs: %w", err)
	}

	return workflowOutputs, nil
}

// executeScatterSubWorkflow executes a scatter over a subworkflow.
func (r *Runner) executeScatterSubWorkflow(ctx context.Context, subGraph *cwl.GraphDocument,
	step cwl.Step, inputs map[string]any, evaluator *cwlexpr.Evaluator) (map[string]any, error) {

	if len(step.Scatter) == 0 {
		return nil, fmt.Errorf("no scatter inputs specified")
	}

	// Determine scatter method.
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over.
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Generate input combinations.
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = dotProduct(inputs, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = nestedCrossProduct(inputs, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = flatCrossProduct(inputs, step.Scatter, scatterArrays)
	default:
		return nil, fmt.Errorf("unknown scatter method: %s", method)
	}

	r.logger.Info("executing scatter over subworkflow", "workflow", subGraph.Workflow.ID, "iterations", len(combinations))

	// Execute each scatter iteration.
	results := make([]map[string]any, len(combinations))
	for i, iterInputs := range combinations {
		// Evaluate valueFrom for this iteration if evaluator is provided.
		if evaluator != nil {
			for inputID, stepInput := range step.In {
				if stepInput.ValueFrom != "" {
					self := iterInputs[inputID]
					ctx := cwlexpr.NewContext(iterInputs).WithSelf(self)
					evaluated, err := evaluator.Evaluate(stepInput.ValueFrom, ctx)
					if err != nil {
						return nil, fmt.Errorf("iteration %d input %s valueFrom: %w", i, inputID, err)
					}
					iterInputs[inputID] = evaluated
				}
			}
		}

		// Check 'when' condition for this iteration.
		if step.When != "" && evaluator != nil {
			evalCtx := cwlexpr.NewContext(iterInputs)
			shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("iteration %d when expression: %w", i, err)
			}
			if !shouldRun {
				r.logger.Debug("skipping scatter iteration (when condition false)", "iteration", i)
				results[i] = nil
				continue
			}
		}

		// Execute the subworkflow for this iteration.
		outputs, err := r.executeSubWorkflow(ctx, subGraph, iterInputs)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: %w", i, err)
		}
		results[i] = outputs
	}

	// Aggregate outputs: merge results into output arrays.
	outputs := make(map[string]any)
	for _, outID := range step.Out {
		arr := make([]any, len(results))
		for i, result := range results {
			if result != nil {
				arr[i] = result[outID]
			}
		}
		outputs[outID] = arr
	}
	return outputs, nil
}

// executeScatterExpressionTool executes a scatter over an ExpressionTool.
func (r *Runner) executeScatterExpressionTool(ctx context.Context, graph *cwl.GraphDocument,
	exprTool *cwl.ExpressionTool, step cwl.Step, inputs map[string]any,
	evaluator *cwlexpr.Evaluator) (map[string]any, error) {

	if len(step.Scatter) == 0 {
		return nil, fmt.Errorf("no scatter inputs specified")
	}

	// Determine scatter method.
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Get the arrays to scatter over.
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := inputs[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			return nil, fmt.Errorf("scatter input %q is not an array", scatterInput)
		}
		scatterArrays[scatterInput] = arr
	}

	// Generate input combinations.
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = dotProduct(inputs, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = nestedCrossProduct(inputs, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = flatCrossProduct(inputs, step.Scatter, scatterArrays)
	default:
		return nil, fmt.Errorf("unknown scatter method: %s", method)
	}

	r.logger.Info("executing scatter over expression tool", "tool", exprTool.ID, "iterations", len(combinations))

	// Execute each scatter iteration.
	results := make([]map[string]any, len(combinations))
	for i, iterInputs := range combinations {
		// Evaluate valueFrom for this iteration if evaluator is provided.
		if evaluator != nil {
			for inputID, stepInput := range step.In {
				if stepInput.ValueFrom != "" {
					self := iterInputs[inputID]
					ctx := cwlexpr.NewContext(iterInputs).WithSelf(self)
					evaluated, err := evaluator.Evaluate(stepInput.ValueFrom, ctx)
					if err != nil {
						return nil, fmt.Errorf("iteration %d input %s valueFrom: %w", i, inputID, err)
					}
					iterInputs[inputID] = evaluated
				}
			}
		}

		// Check 'when' condition for this iteration.
		if step.When != "" && evaluator != nil {
			evalCtx := cwlexpr.NewContext(iterInputs)
			shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
			if err != nil {
				return nil, fmt.Errorf("iteration %d when expression: %w", i, err)
			}
			if !shouldRun {
				r.logger.Debug("skipping scatter iteration (when condition false)", "iteration", i)
				results[i] = nil
				continue
			}
		}

		// Execute the expression tool for this iteration.
		outputs, err := r.executeExpressionTool(exprTool, iterInputs, graph)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: %w", i, err)
		}
		results[i] = outputs
	}

	// Aggregate outputs: merge results into output arrays.
	outputs := make(map[string]any)
	for _, outID := range step.Out {
		arr := make([]any, len(results))
		for i, result := range results {
			if result != nil {
				arr[i] = result[outID]
			}
		}
		outputs[outID] = arr
	}
	return outputs, nil
}

// executeWorkflow executes a workflow.
func (r *Runner) executeWorkflow(ctx context.Context, graph *cwl.GraphDocument, inputs map[string]any, w io.Writer) error {
	r.logger.Info("executing workflow", "id", graph.Workflow.ID, "parallel", r.Parallel.Enabled)

	// Merge workflow input defaults with provided inputs.
	mergedInputs := mergeWorkflowInputDefaults(graph.Workflow, inputs, r.cwlDir)

	// Build execution order using DAG.
	dag, err := parser.BuildDAG(graph.Workflow)
	if err != nil {
		return fmt.Errorf("build DAG: %w", err)
	}

	// Set metrics for workflow
	r.metrics.SetWorkflowID(graph.Workflow.ID)
	r.metrics.SetTotalSteps(len(dag.Order))

	// Use parallel execution if enabled
	if r.Parallel.Enabled {
		pe := newParallelExecutor(r, graph, dag, mergedInputs, r.Parallel)
		workflowOutputs, err := pe.execute(ctx)
		if err != nil {
			r.finalizeAndPrintMetrics(w)
			return err
		}
		return r.writeOutputsWithMetrics(workflowOutputs, w)
	}

	// Sequential execution (original behavior)
	return r.executeWorkflowSequential(ctx, graph, dag, mergedInputs, w)
}

// executeWorkflowSequential executes workflow steps sequentially.
func (r *Runner) executeWorkflowSequential(ctx context.Context, graph *cwl.GraphDocument,
	dag *parser.DAGResult, mergedInputs map[string]any, w io.Writer) error {

	// Track outputs from completed steps.
	stepOutputs := make(map[string]map[string]any)

	// Create evaluator for expressions (valueFrom, when).
	evaluator := cwlexpr.NewEvaluator(extractExpressionLib(graph, r.cwlDir))

	// Execute steps in topological order.
	for _, stepID := range dag.Order {
		step := graph.Workflow.Steps[stepID]

		// For scattered steps, defer valueFrom evaluation to after scatter expansion.
		// For non-scattered steps, evaluate valueFrom now.
		var stepEvaluator *cwlexpr.Evaluator
		if len(step.Scatter) == 0 {
			stepEvaluator = evaluator
		}
		stepInputs, err := resolveStepInputs(step, mergedInputs, stepOutputs, r.cwlDir, stepEvaluator)
		if err != nil {
			return fmt.Errorf("step %s: %w", stepID, err)
		}

		// Check if this is an ExpressionTool.
		toolRef := stripHash(step.Run)
		if exprTool, ok := graph.ExpressionTools[toolRef]; ok {
			// Handle scatter if present.
			if len(step.Scatter) > 0 {
				outputs, err := r.executeScatterExpressionTool(ctx, graph, exprTool, step, stepInputs, evaluator)
				if err != nil {
					r.finalizeAndPrintMetrics(w)
					return fmt.Errorf("step %s: %w", stepID, err)
				}
				stepOutputs[stepID] = outputs
				continue
			}

			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					// Record skipped step metrics
					if r.metrics != nil && r.metrics.Enabled() {
						r.metrics.RecordStep(StepMetrics{
							StepID: stepID,
							ToolID: exprTool.ID,
							Status: "skipped",
						})
					}
					continue
				}
			}

			outputs, err := r.executeExpressionTool(exprTool, stepInputs, graph)
			if err != nil {
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
			continue
		}

		// Check if this is a SubWorkflow.
		if subGraph, ok := graph.SubWorkflows[toolRef]; ok {
			// Handle scatter if present.
			if len(step.Scatter) > 0 {
				outputs, err := r.executeScatterSubWorkflow(ctx, subGraph, step, stepInputs, evaluator)
				if err != nil {
					r.finalizeAndPrintMetrics(w)
					return fmt.Errorf("step %s: %w", stepID, err)
				}
				stepOutputs[stepID] = outputs
				continue
			}

			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					continue
				}
			}

			r.logger.Info("executing subworkflow", "step", stepID, "workflow", toolRef)

			// Execute the subworkflow recursively.
			outputs, err := r.executeSubWorkflow(ctx, subGraph, stepInputs)
			if err != nil {
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
			continue
		}

		// Otherwise it's a CommandLineTool.
		tool := graph.Tools[toolRef]
		if tool == nil {
			return fmt.Errorf("step %s: tool %s not found", stepID, step.Run)
		}

		// Merge step requirements into tool (step requirements override tool hints).
		mergeStepRequirements(tool, &step)

		// Handle scatter if present.
		// Note: 'when' condition is evaluated per-iteration inside executeScatter.
		if len(step.Scatter) > 0 {
			outputs, err := r.executeScatterWithMetrics(ctx, graph, tool, step, stepInputs, stepID, evaluator)
			if err != nil {
				r.finalizeAndPrintMetrics(w)
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		} else {
			// Handle conditional execution for non-scattered steps.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				shouldRun, err := evaluator.EvaluateBool(step.When, evalCtx)
				if err != nil {
					return fmt.Errorf("step %s when expression: %w", stepID, err)
				}
				if !shouldRun {
					r.logger.Info("skipping step (when condition false)", "step", stepID)
					stepOutputs[stepID] = make(map[string]any)
					// Record skipped step metrics
					if r.metrics != nil && r.metrics.Enabled() {
						r.metrics.RecordStep(StepMetrics{
							StepID: stepID,
							ToolID: tool.ID,
							Status: "skipped",
						})
					}
					continue
				}
			}

			outputs, err := r.executeToolWithStepID(ctx, graph, tool, stepInputs, false, stepID)
			if err != nil {
				r.finalizeAndPrintMetrics(w)
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		}
	}

	// Collect workflow outputs (pass inputs for passthrough workflows).
	workflowOutputs, err := collectWorkflowOutputs(graph.Workflow, mergedInputs, stepOutputs)
	if err != nil {
		return fmt.Errorf("collect outputs: %w", err)
	}
	return r.writeOutputsWithMetrics(workflowOutputs, w)
}

// writeOutputs writes the outputs to the writer in the configured format.
func (r *Runner) writeOutputs(outputs map[string]any, w io.Writer) error {
	return r.writeOutputsInternal(outputs, nil, w)
}

// writeOutputsInternal writes the outputs to the writer with optional metrics.
func (r *Runner) writeOutputsInternal(outputs map[string]any, metricsMap map[string]any, w io.Writer) error {
	var data []byte
	var err error

	// If metrics are provided and format is JSON, include them in the output.
	outputWithMetrics := outputs
	if metricsMap != nil && r.OutputFormat != "yaml" {
		outputWithMetrics = make(map[string]any)
		for k, v := range outputs {
			outputWithMetrics[k] = v
		}
		outputWithMetrics["cwl:metrics"] = metricsMap
	}

	switch r.OutputFormat {
	case "yaml":
		data, err = yaml.Marshal(outputs)
	default:
		// Convert floats to json.Number to avoid scientific notation.
		converted := cwl.ConvertForCWLOutput(outputWithMetrics)
		data, err = json.MarshalIndent(converted, "", "  ")
	}

	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("write outputs: %w", err)
	}
	fmt.Fprintln(w)
	return nil
}

// Note: Float-to-number conversion for CWL output is now in pkg/cwl/json.go
// as cwl.ConvertForCWLOutput to be shared with the CLI.

// DAGOutput represents the DAG structure for JSON output.
type DAGOutput struct {
	Workflow  string               `json:"workflow"`
	Steps     map[string]StepInfo  `json:"steps"`
	Order     []string             `json:"execution_order"`
	Inputs    map[string]any       `json:"inputs"`
}

// StepInfo describes a workflow step.
type StepInfo struct {
	Tool      string   `json:"tool"`
	DependsOn []string `json:"depends_on"`
	Scatter   []string `json:"scatter,omitempty"`
	When      string   `json:"when,omitempty"`
}

// buildDAGOutput builds a DAG representation for output.
func buildDAGOutput(graph *cwl.GraphDocument, inputs map[string]any) *DAGOutput {
	dag, err := parser.BuildDAG(graph.Workflow)
	if err != nil {
		return nil
	}

	output := &DAGOutput{
		Workflow: graph.Workflow.ID,
		Steps:    make(map[string]StepInfo),
		Order:    dag.Order,
		Inputs:   inputs,
	}

	for stepID, step := range graph.Workflow.Steps {
		info := StepInfo{
			Tool:    step.Run,
			Scatter: step.Scatter,
			When:    step.When,
		}

		// Get dependencies from DAG edges (Edges[stepID] = [deps...]).
		info.DependsOn = dag.Edges[stepID]

		output.Steps[stepID] = info
	}

	return output
}

// resolveInputPaths resolves file paths relative to the job file directory.
func resolveInputPaths(inputs map[string]any, baseDir string) map[string]any {
	resolved := make(map[string]any)
	for k, v := range inputs {
		resolved[k] = resolveInputValue(v, baseDir)
	}
	return resolved
}

// resolveInputValue recursively resolves file paths.
func resolveInputValue(v any, baseDir string) any {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				return resolveFileObject(val, baseDir)
			}
		}
		// Recursively resolve nested maps.
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = resolveInputValue(v, baseDir)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = resolveInputValue(item, baseDir)
		}
		return resolved
	default:
		return v
	}
}

// resolveFileObject resolves a File or Directory path.
func resolveFileObject(obj map[string]any, baseDir string) map[string]any {
	resolved := make(map[string]any)
	for k, v := range obj {
		resolved[k] = v
	}

	// Handle file literals: File objects with "contents" but no path/location.
	// These need to be materialized as actual files.
	if _, err := fileliteral.MaterializeFileObject(resolved); err != nil {
		// Log error but continue - file literals are not critical.
		_ = err
	}

	// Step 1: Resolve location (make it absolute if relative).
	if loc, ok := resolved["location"].(string); ok {
		if !filepath.IsAbs(loc) && !isURI(loc) {
			resolved["location"] = filepath.Join(baseDir, loc)
		}
	}

	// Step 2: Resolve path (make it absolute if relative).
	if path, ok := resolved["path"].(string); ok {
		if !filepath.IsAbs(path) {
			resolved["path"] = filepath.Join(baseDir, path)
		}
	}

	// Step 3: If path not set, derive from location.
	if _, hasPath := resolved["path"]; !hasPath {
		if loc, ok := resolved["location"].(string); ok {
			// Strip file:// prefix if present.
			var path string
			if strings.HasPrefix(loc, "file://") {
				path = loc[7:]
			} else if !strings.Contains(loc, "://") {
				path = loc
			}
			// URL-decode the path (handle %23 -> # etc).
			if path != "" {
				path = cwl.DecodePath(path)
				resolved["path"] = path
			}
		}
	}

	// Step 4: Final check - ensure path is absolute.
	// Note: path has already been joined with baseDir in steps 2 or 3,
	// so we only need to call Abs() to resolve the full path.
	if path, ok := resolved["path"].(string); ok && !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err == nil {
			resolved["path"] = absPath
		}
	}

	// Step 5: Compute basename, dirname, nameroot, nameext if path is available.
	if path, ok := resolved["path"].(string); ok && path != "" {
		if _, hasBasename := resolved["basename"]; !hasBasename {
			resolved["basename"] = filepath.Base(path)
		}
		if _, hasDirname := resolved["dirname"]; !hasDirname {
			resolved["dirname"] = filepath.Dir(path)
		}
		basename := filepath.Base(path)
		if _, hasNameroot := resolved["nameroot"]; !hasNameroot {
			nameroot, nameext := splitBasenameExt(basename)
			resolved["nameroot"] = nameroot
			resolved["nameext"] = nameext
		}

		// Step 5b: Populate size for File objects if not already set.
		class, _ := resolved["class"].(string)
		if class == "File" {
			if _, hasSize := resolved["size"]; !hasSize {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					resolved["size"] = info.Size()
				}
			}
		}
	}

	// Step 6: For Directory objects, resolve listing entries.
	if listing, ok := resolved["listing"].([]any); ok {
		// Get the directory path for resolving relative paths in listing.
		dirPath := baseDir
		if path, ok := resolved["path"].(string); ok {
			dirPath = path
		}
		resolvedListing := make([]any, len(listing))
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				resolvedListing[i] = resolveFileObject(itemMap, dirPath)
			} else {
				resolvedListing[i] = item
			}
		}
		resolved["listing"] = resolvedListing
	}

	// Step 7: For File objects, resolve secondaryFiles entries.
	// Secondary file locations from job files are relative to the job file's baseDir,
	// not the primary file's directory. This matches CWL spec behavior.
	if secFiles, ok := resolved["secondaryFiles"].([]any); ok {
		resolvedSecFiles := make([]any, len(secFiles))
		for i, item := range secFiles {
			if itemMap, ok := item.(map[string]any); ok {
				resolvedSecFiles[i] = resolveFileObject(itemMap, baseDir)
			} else {
				resolvedSecFiles[i] = item
			}
		}
		resolved["secondaryFiles"] = resolvedSecFiles
	}

	return resolved
}

// splitBasenameExt splits a filename into nameroot and nameext.
func splitBasenameExt(basename string) (string, string) {
	for i := len(basename) - 1; i > 0; i-- {
		if basename[i] == '.' {
			return basename[:i], basename[i:]
		}
	}
	return basename, ""
}

// isURI checks if a string is a URI.
func isURI(s string) bool {
	return len(s) > 5 && (s[:5] == "file:" || s[:5] == "http:" || s[:6] == "https:")
}

// resolveStepInputs resolves inputs for a workflow step.
// cwlDir is used to resolve relative paths in step input defaults.
// evaluator is optional; if provided, valueFrom expressions will be evaluated.
// For scattered steps, pass evaluator=nil to defer valueFrom to after scatter expansion,
// then call evaluateValueFrom on each scattered combination.
func resolveStepInputs(step cwl.Step, workflowInputs map[string]any, stepOutputs map[string]map[string]any, cwlDir string, evaluator *cwlexpr.Evaluator) (map[string]any, error) {
	resolved := make(map[string]any)

	// First pass: resolve sources and defaults.
	for inputID, stepInput := range step.In {
		var value any
		if len(stepInput.Sources) == 1 {
			// Single source - value is the resolved source.
			value = resolveSource(stepInput.Sources[0], workflowInputs, stepOutputs)
		} else if len(stepInput.Sources) > 1 {
			// Multiple sources (MultipleInputFeatureRequirement) - value is array of resolved sources.
			values := make([]any, len(stepInput.Sources))
			for i, src := range stepInput.Sources {
				values[i] = resolveSource(src, workflowInputs, stepOutputs)
			}
			// Apply linkMerge to combine values.
			value = cwloutput.ApplyLinkMerge(values, stepInput.LinkMerge)
		}
		if value == nil && stepInput.Default != nil {
			// Resolve File/Directory objects in defaults relative to CWL directory.
			value = resolveDefaultValue(stepInput.Default, cwlDir)
		}
		resolved[inputID] = value
	}

	// Apply loadContents for step inputs that have it enabled.
	// This happens before valueFrom so expressions can access self.contents.
	for inputID, stepInput := range step.In {
		if stepInput.LoadContents {
			if val := resolved[inputID]; val != nil {
				resolved[inputID] = applyLoadContents(val, cwlDir)
			}
		}
	}

	// Third pass: evaluate valueFrom expressions with the step's resolved inputs as context.
	// Per CWL spec, valueFrom has access to `inputs` (the step's own inputs) and `self`
	// (the resolved source value before transformation).
	if evaluator != nil {
		if err := evaluateValueFrom(step, resolved, evaluator, workflowInputs); err != nil {
			return nil, err
		}
	}

	return resolved, nil
}

// evaluateValueFrom evaluates valueFrom expressions on step inputs.
// The `inputs` context contains the step's resolved inputs (post-scatter for scattered steps).
// workflowInputs are merged in as well so expressions can reference any workflow input.
// `self` is set to the pre-valueFrom value of the current input.
// Per CWL spec, `inputs` provides the source-resolved values (before valueFrom transformation),
// so all valueFrom expressions see the same snapshot of input values.
func evaluateValueFrom(step cwl.Step, resolved map[string]any, evaluator *cwlexpr.Evaluator, workflowInputs ...map[string]any) error {
	// Build the inputs context: workflow inputs as base, step inputs override.
	// This snapshot is used for ALL valueFrom evaluations (not updated between them).
	inputsCtx := make(map[string]any)
	if len(workflowInputs) > 0 && workflowInputs[0] != nil {
		for k, v := range workflowInputs[0] {
			inputsCtx[k] = v
		}
	}
	for k, v := range resolved {
		inputsCtx[k] = v
	}

	for inputID, stepInput := range step.In {
		if stepInput.ValueFrom == "" {
			continue
		}
		self := resolved[inputID]
		ctx := cwlexpr.NewContext(inputsCtx).WithSelf(self)
		evaluated, err := evaluator.Evaluate(stepInput.ValueFrom, ctx)
		if err != nil {
			return fmt.Errorf("input %s valueFrom: %w", inputID, err)
		}
		resolved[inputID] = evaluated
		// Note: We intentionally do NOT update inputsCtx here.
		// Per CWL spec, `inputs` in valueFrom expressions should contain the
		// source-resolved values (before valueFrom), not transformed values.
	}
	return nil
}

// populateDirectoryListings adds listing entries to Directory inputs based on loadListing.
// It checks both per-input loadListing and the LoadListingRequirement at tool level.
func populateDirectoryListings(tool *cwl.CommandLineTool, inputs map[string]any) {
	// Get default loadListing from LoadListingRequirement.
	defaultLoadListing := ""
	if tool.Requirements != nil {
		if llr, ok := tool.Requirements["LoadListingRequirement"].(map[string]any); ok {
			if ll, ok := llr["loadListing"].(string); ok {
				defaultLoadListing = ll
			}
		}
	}

	for inputID, inp := range tool.Inputs {
		// Per-input loadListing overrides the default.
		loadListing := inp.LoadListing
		if loadListing == "" {
			loadListing = defaultLoadListing
		}
		if loadListing == "" || loadListing == "no_listing" {
			continue
		}

		inputVal, ok := inputs[inputID]
		if !ok || inputVal == nil {
			continue
		}

		// Handle single Directory input.
		if dirObj, ok := inputVal.(map[string]any); ok {
			if class, _ := dirObj["class"].(string); class == "Directory" {
				populateDirListing(dirObj, loadListing)
			}
		}
		// Handle array of Directories.
		if arr, ok := inputVal.([]any); ok {
			for _, item := range arr {
				if dirObj, ok := item.(map[string]any); ok {
					if class, _ := dirObj["class"].(string); class == "Directory" {
						populateDirListing(dirObj, loadListing)
					}
				}
			}
		}
	}
}

// populateDirListing reads the filesystem and populates a Directory object's listing.
func populateDirListing(dirObj map[string]any, depth string) {
	dirPath, _ := dirObj["path"].(string)
	if dirPath == "" {
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}

	var listing []any
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			child := map[string]any{
				"class":    "Directory",
				"location": "file://" + entryPath,
				"path":     entryPath,
				"basename": entry.Name(),
			}
			if depth == "deep_listing" {
				populateDirListing(child, depth)
			}
			listing = append(listing, child)
		} else {
			info, err := entry.Info()
			size := int64(0)
			if err == nil {
				size = info.Size()
			}
			child := map[string]any{
				"class":    "File",
				"location": "file://" + entryPath,
				"path":     entryPath,
				"basename": entry.Name(),
				"size":     size,
			}
			listing = append(listing, child)
		}
	}
	dirObj["listing"] = listing
}

// populateExpressionToolDirectoryListings adds listing entries to Directory inputs for ExpressionTools.
// It checks both per-input loadListing and the LoadListingRequirement at tool level.
func populateExpressionToolDirectoryListings(tool *cwl.ExpressionTool, inputs map[string]any) {
	// Get default loadListing from LoadListingRequirement.
	defaultLoadListing := ""
	if tool.Requirements != nil {
		if llr, ok := tool.Requirements["LoadListingRequirement"].(map[string]any); ok {
			if ll, ok := llr["loadListing"].(string); ok {
				defaultLoadListing = ll
			}
		}
	}

	for inputID, inp := range tool.Inputs {
		// Per-input loadListing overrides the default.
		loadListing := inp.LoadListing
		if loadListing == "" {
			loadListing = defaultLoadListing
		}
		if loadListing == "" || loadListing == "no_listing" {
			continue
		}

		inputVal, ok := inputs[inputID]
		if !ok || inputVal == nil {
			continue
		}

		// Handle single Directory input.
		if dirObj, ok := inputVal.(map[string]any); ok {
			if class, _ := dirObj["class"].(string); class == "Directory" {
				populateDirListing(dirObj, loadListing)
			}
		}
		// Handle array of Directories.
		if arr, ok := inputVal.([]any); ok {
			for _, item := range arr {
				if dirObj, ok := item.(map[string]any); ok {
					if class, _ := dirObj["class"].(string); class == "Directory" {
						populateDirListing(dirObj, loadListing)
					}
				}
			}
		}
	}
}

// resolveSource resolves a source reference to its value.
func resolveSource(source string, workflowInputs map[string]any, stepOutputs map[string]map[string]any) any {
	if source == "" {
		return nil
	}

	// Check if it's a step output reference (step_id/output_id).
	for i := 0; i < len(source); i++ {
		if source[i] == '/' {
			stepID := source[:i]
			outputID := source[i+1:]
			if outputs, ok := stepOutputs[stepID]; ok {
				return outputs[outputID]
			}
			return nil
		}
	}

	// Otherwise it's a workflow input reference.
	return workflowInputs[source]
}

// applyLoadContents reads the first 64 KiB of a file and adds it to the contents field.
// This implements CWL's loadContents feature for File objects.
func applyLoadContents(value any, cwlDir string) any {
	switch v := value.(type) {
	case map[string]any:
		// Check if this is a File object.
		if class, ok := v["class"].(string); ok && class == "File" {
			// Get the file path.
			path := ""
			if p, ok := v["path"].(string); ok {
				path = p
			} else if p, ok := v["location"].(string); ok {
				path = p
			}
			if path == "" {
				return value
			}

			// Handle file:// URLs.
			if strings.HasPrefix(path, "file://") {
				path = strings.TrimPrefix(path, "file://")
			}

			// Make path absolute if needed.
			if !filepath.IsAbs(path) && cwlDir != "" {
				path = filepath.Join(cwlDir, path)
			}

			// Read up to 64 KiB of the file.
			const maxSize = 64 * 1024
			data, err := os.ReadFile(path)
			if err != nil {
				return value // Return unchanged if we can't read.
			}
			if len(data) > maxSize {
				data = data[:maxSize]
			}

			// Create a new map with contents field.
			result := make(map[string]any, len(v)+1)
			for k, val := range v {
				result[k] = val
			}
			result["contents"] = string(data)
			return result
		}
		return value
	case []any:
		// Handle arrays of files.
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = applyLoadContents(item, cwlDir)
		}
		return result
	default:
		return value
	}
}

// collectWorkflowOutputs collects outputs from completed steps or passthrough from inputs.
// Supports multiple sources with linkMerge and pickValue for conditional workflows.
// Uses shared cwloutput package for linkMerge and pickValue logic.
func collectWorkflowOutputs(wf *cwl.Workflow, workflowInputs map[string]any, stepOutputs map[string]map[string]any) (map[string]any, error) {
	outputs := make(map[string]any)
	for outputID, output := range wf.Outputs {
		// Collect all sources.
		var sources []string
		if output.OutputSource != "" {
			sources = []string{output.OutputSource}
		} else {
			sources = output.OutputSources
		}

		// Resolve all source values.
		var values []any
		for _, src := range sources {
			values = append(values, resolveSource(src, workflowInputs, stepOutputs))
		}

		// Apply linkMerge if multiple sources.
		if len(sources) > 1 {
			values = cwloutput.ApplyLinkMerge(values, output.LinkMerge)
		}

		// Handle scatter outputs with pickValue.
		// If single source is an array (from scatter), apply pickValue to array elements.
		if len(sources) == 1 && output.PickValue != "" {
			if arr, ok := values[0].([]any); ok {
				values = arr
			}
		}

		// Apply pickValue using shared package.
		result, err := cwloutput.ApplyPickValue(values, output.PickValue)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", outputID, err)
		}
		outputs[outputID] = result
	}
	return outputs, nil
}

// extractExpressionLib extracts the expression library from requirements.
func extractExpressionLib(graph *cwl.GraphDocument, cwlDir string) []string {
	// Check workflow requirements.
	if graph.Workflow != nil {
		// Requirements not yet stored in Workflow struct.
	}

	// Check tool requirements.
	for _, tool := range graph.Tools {
		if tool.Requirements != nil {
			if ijsReq, ok := tool.Requirements["InlineJavascriptRequirement"].(map[string]any); ok {
				if lib, ok := ijsReq["expressionLib"].([]any); ok {
					var result []string
					for _, item := range lib {
						switch v := item.(type) {
						case string:
							result = append(result, v)
						case map[string]any:
							// Handle $include directive: { $include: "filename.js" }
							if includePath, ok := v["$include"].(string); ok {
								fullPath := filepath.Join(cwlDir, includePath)
								data, err := os.ReadFile(fullPath)
								if err == nil {
									result = append(result, string(data))
								}
							}
						}
					}
					return result
				}
			}
		}
	}

	return nil
}

// extractExpressionLibFromTool extracts expression library from a single tool.
func extractExpressionLibFromTool(tool *cwl.CommandLineTool) []string {
	if tool.Requirements == nil {
		return nil
	}
	ijsReq, ok := tool.Requirements["InlineJavascriptRequirement"].(map[string]any)
	if !ok {
		return nil
	}
	lib, ok := ijsReq["expressionLib"].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range lib {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// hasDockerRequirement checks if a tool or workflow has a DockerRequirement.
// It checks tool requirements, tool hints, workflow requirements, and workflow hints.
func hasDockerRequirement(tool *cwl.CommandLineTool, wf *cwl.Workflow) bool {
	// Check tool requirements first.
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	// Then tool hints.
	if tool.Hints != nil {
		if _, ok := tool.Hints["DockerRequirement"]; ok {
			return true
		}
	}
	// Then workflow requirements (inherited by steps).
	if wf != nil && wf.Requirements != nil {
		if _, ok := wf.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	// Then workflow hints (inherited by steps).
	if wf != nil && wf.Hints != nil {
		if _, ok := wf.Hints["DockerRequirement"]; ok {
			return true
		}
	}
	return false
}

// getDockerImage extracts the Docker image from requirements or hints.
// It checks tool first, then workflow-level hints if present.
func getDockerImage(tool *cwl.CommandLineTool, wf *cwl.Workflow) string {
	// Check tool requirements first.
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	// Then tool hints.
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	// Then workflow requirements.
	if wf != nil && wf.Requirements != nil {
		if dr, ok := wf.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	// Then workflow hints.
	if wf != nil && wf.Hints != nil {
		if dr, ok := wf.Hints["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	return ""
}

// getDockerOutputDirectory extracts dockerOutputDirectory from DockerRequirement.
// Returns the custom output directory path if specified, empty string otherwise.
func getDockerOutputDirectory(tool *cwl.CommandLineTool) string {
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if outputDir, ok := dr["dockerOutputDirectory"].(string); ok {
				return outputDir
			}
		}
	}
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if outputDir, ok := dr["dockerOutputDirectory"].(string); ok {
				return outputDir
			}
		}
	}
	return ""
}

// getResourceRequirement extracts ResourceRequirement from hints or requirements.
func getResourceRequirement(tool *cwl.CommandLineTool) map[string]any {
	// Check requirements first.
	if tool.Requirements != nil {
		if rr, ok := tool.Requirements["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	// Then hints.
	if tool.Hints != nil {
		if rr, ok := tool.Hints["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	return nil
}

// hasInplaceUpdateRequirement checks if InplaceUpdateRequirement is enabled.
// When enabled, writable files in InitialWorkDirRequirement should be symlinked
// rather than copied, allowing modifications to affect the original files.
func hasInplaceUpdateRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements == nil {
		return false
	}
	iur, ok := tool.Requirements["InplaceUpdateRequirement"].(map[string]any)
	if !ok {
		return false
	}
	inplaceUpdate, _ := iur["inplaceUpdate"].(bool)
	return inplaceUpdate
}

// getToolTimeLimit extracts the ToolTimeLimit timelimit value in seconds.
// Returns 0 if not specified or if value is <= 0 (meaning no limit).
// Supports expression evaluation for dynamic timelimit values.
func getToolTimeLimit(tool *cwl.CommandLineTool, inputs map[string]any) int {
	// Check requirements first (ToolTimeLimit is a requirement, not a hint).
	if tool.Requirements == nil {
		return 0
	}
	ttl, ok := tool.Requirements["ToolTimeLimit"].(map[string]any)
	if !ok {
		return 0
	}
	limit, ok := ttl["timelimit"]
	if !ok {
		return 0
	}

	var result int
	switch v := limit.(type) {
	case int:
		result = v
	case float64:
		result = int(v)
	case string:
		// Handle expression like $(1+2)
		if cwlexpr.IsExpression(v) {
			expressionLib := extractExpressionLibFromTool(tool)
			evaluator := cwlexpr.NewEvaluator(expressionLib)
			ctx := cwlexpr.NewContext(inputs)
			evaluated, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return 0
			}
			switch ev := evaluated.(type) {
			case int:
				result = ev
			case int64:
				result = int(ev)
			case float64:
				result = int(ev)
			default:
				return 0
			}
		}
	}

	// Return the result (negative values will be handled by caller)
	return result
	return 0
}

// mergeWorkflowRequirements merges workflow-level requirements into the tool.
// Workflow requirements override tool hints, but tool requirements take precedence.
// This ensures proper requirement inheritance per CWL spec.
func mergeWorkflowRequirements(tool *cwl.CommandLineTool, wf *cwl.Workflow) {
	if wf == nil {
		return
	}

	// Merge workflow requirements into tool requirements.
	// Workflow requirements override tool hints but not tool requirements.
	if wf.Requirements != nil {
		if tool.Requirements == nil {
			tool.Requirements = make(map[string]any)
		}
		for key, val := range wf.Requirements {
			// Only add if not already in tool requirements.
			if _, exists := tool.Requirements[key]; !exists {
				tool.Requirements[key] = val
			}
		}
	}

	// Merge workflow hints into tool hints (lowest priority).
	if wf.Hints != nil {
		if tool.Hints == nil {
			tool.Hints = make(map[string]any)
		}
		for key, val := range wf.Hints {
			// Only add if not already in tool requirements or hints.
			if _, exists := tool.Requirements[key]; !exists {
				if _, exists := tool.Hints[key]; !exists {
					tool.Hints[key] = val
				}
			}
		}
	}
}

// mergeStepRequirements merges step-level requirements into the tool.
// Step requirements override tool hints, but tool requirements take precedence.
// This is called before mergeWorkflowRequirements for proper CWL priority:
// tool requirements > step requirements > workflow requirements > tool hints > step hints > workflow hints
func mergeStepRequirements(tool *cwl.CommandLineTool, step *cwl.Step) {
	if step == nil {
		return
	}

	// Merge step requirements into tool requirements.
	// Step requirements override tool hints but not tool requirements.
	if step.Requirements != nil {
		if tool.Requirements == nil {
			tool.Requirements = make(map[string]any)
		}
		for key, val := range step.Requirements {
			// Only add if not already in tool requirements.
			if _, exists := tool.Requirements[key]; !exists {
				tool.Requirements[key] = val
			}
		}
	}

	// Merge step hints into tool hints.
	if step.Hints != nil {
		if tool.Hints == nil {
			tool.Hints = make(map[string]any)
		}
		for key, val := range step.Hints {
			// Only add if not already in tool requirements or hints.
			if _, exists := tool.Requirements[key]; !exists {
				if _, exists := tool.Hints[key]; !exists {
					tool.Hints[key] = val
				}
			}
		}
	}
}

// buildRuntimeContext creates a RuntimeContext from tool requirements.
func buildRuntimeContext(tool *cwl.CommandLineTool, outDir string) *cwlexpr.RuntimeContext {
	return buildRuntimeContextWithInputs(tool, outDir, nil, nil)
}

// buildRuntimeContextWithInputs creates a RuntimeContext, evaluating expressions if inputs are provided.
func buildRuntimeContextWithInputs(tool *cwl.CommandLineTool, outDir string, inputs map[string]any, expressionLib []string) *cwlexpr.RuntimeContext {
	runtime := cwlexpr.DefaultRuntimeContext()
	runtime.OutDir = outDir
	runtime.TmpDir = outDir + "_tmp" // Must match what executeLocal creates

	// Apply ResourceRequirement if present.
	rr := getResourceRequirement(tool)
	if rr != nil {
		// Create evaluator if we have inputs and expressions.
		var evaluator *cwlexpr.Evaluator
		if inputs != nil {
			evaluator = cwlexpr.NewEvaluator(expressionLib)
		}
		ctx := cwlexpr.NewContext(inputs)

		// CWL allows coresMin/coresMax - use coresMin if present.
		if coresMin, ok := rr["coresMin"]; ok {
			runtime.Cores = evalResourceInt(coresMin, evaluator, ctx)
		}
		// If no coresMin, try cores.
		if runtime.Cores == 1 {
			if cores, ok := rr["cores"]; ok {
				runtime.Cores = evalResourceInt(cores, evaluator, ctx)
			}
		}

		// Apply RAM requirements.
		if ramMin, ok := rr["ramMin"]; ok {
			runtime.Ram = int64(evalResourceInt(ramMin, evaluator, ctx))
		}

		// Apply tmpdir size requirements.
		if tmpdirMin, ok := rr["tmpdirMin"]; ok {
			runtime.TmpdirSize = int64(evalResourceInt(tmpdirMin, evaluator, ctx))
		}

		// Apply outdir size requirements.
		if outdirMin, ok := rr["outdirMin"]; ok {
			runtime.OutdirSize = int64(evalResourceInt(outdirMin, evaluator, ctx))
		}
	}

	return runtime
}

// evalResourceInt evaluates a resource value that may be int, float, or expression.
func evalResourceInt(value any, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(math.Ceil(v)) // CWL spec: float cores are rounded up
	case string:
		// Expression - evaluate it if we have an evaluator.
		if evaluator != nil && ctx != nil && (strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "${")) {
			result, err := evaluator.Evaluate(v, ctx)
			if err == nil {
				return evalResourceInt(result, nil, nil) // Recursively convert result
			}
		}
	}
	return 1 // Default fallback
}

// stripHash removes the leading "#" from a tool reference and converts
// external file references to tool IDs.
func stripHash(ref string) string {
	if len(ref) > 0 && ref[0] == '#' {
		return ref[1:]
	}
	// For external file references (*.cwl), extract the tool ID from filename.
	if strings.HasSuffix(ref, ".cwl") {
		base := filepath.Base(ref)
		return strings.TrimSuffix(base, ".cwl")
	}
	return ref
}

// mergeToolDefaults merges tool input defaults with provided inputs.
// Defaults are only used for inputs not provided in the job file.
// Only inputs declared in the tool's inputs are included; undeclared inputs are ignored.
// This is per CWL v1.2 spec: step inputs not declared in the tool are not passed to the tool.
// Also processes loadContents for File inputs (with 64KB limit).
func mergeToolDefaults(tool *cwl.CommandLineTool, inputs map[string]any, cwlDir string) (map[string]any, error) {
	merged := make(map[string]any)

	// Only include inputs that are declared in the tool's inputs.
	for inputID, inputDef := range tool.Inputs {
		var val any
		if v, exists := inputs[inputID]; exists && v != nil {
			// Use provided value if it's not null.
			val = v
		} else if inputDef.Default != nil {
			// Use default value if input not provided or is null.
			val = resolveDefaultValue(inputDef.Default, cwlDir)
		} else if v, exists := inputs[inputID]; exists {
			// Explicitly provided null with no default - keep null.
			val = v
		}

		// Process loadContents for File inputs (with 64KB limit).
		if val != nil && inputDef.LoadContents {
			processedVal, err := loadcontents.Process(val, cwlDir)
			if err != nil {
				return nil, fmt.Errorf("input %q: %w", inputID, err)
			}
			val = processedVal
		}

		// Validate secondaryFiles requirements.
		if val != nil {
			if err := secondaryfiles.ValidateInput(inputID, inputDef, val, merged); err != nil {
				return nil, err
			}
		}

		merged[inputID] = val
	}

	return merged, nil
}

// Note: secondaryFiles validation is now in internal/secondaryfiles package
// as secondaryfiles.ValidateInput to be shared with the execution engine.

// Note: loadContents processing is now in internal/loadcontents package
// as loadcontents.Process to be shared with the execution engine.

// resolveDefaultValue resolves a default value, handling File objects specially.
func resolveDefaultValue(v any, cwlDir string) any {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				// Resolve File/Directory paths relative to CWL file.
				return resolveFileObject(val, cwlDir)
			}
		}
		// Recursively resolve nested maps.
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = resolveDefaultValue(v, cwlDir)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = resolveDefaultValue(item, cwlDir)
		}
		return resolved
	default:
		return v
	}
}

// mergeWorkflowInputDefaults merges workflow input defaults with provided inputs.
// Also resolves secondaryFiles for inputs based on the workflow's input declarations.
func mergeWorkflowInputDefaults(wf *cwl.Workflow, inputs map[string]any, cwlDir string) map[string]any {
	merged := make(map[string]any)

	// Copy provided inputs.
	for k, v := range inputs {
		merged[k] = v
	}

	// Add defaults for missing inputs.
	for inputID, inputDef := range wf.Inputs {
		if _, exists := merged[inputID]; !exists && inputDef.Default != nil {
			// Resolve default value (especially File objects).
			defaultVal := resolveDefaultValue(inputDef.Default, cwlDir)
			merged[inputID] = defaultVal
		}
	}

	// Resolve secondaryFiles for inputs based on workflow input declarations.
	for inputID, inputDef := range wf.Inputs {
		if val, exists := merged[inputID]; exists && val != nil {
			merged[inputID] = resolveInputSecondaryFiles(val, inputDef, cwlDir)
		}
	}

	// Apply loadContents for workflow inputs that have it enabled.
	for inputID, inputDef := range wf.Inputs {
		if inputDef.LoadContents {
			if val, exists := merged[inputID]; exists && val != nil {
				merged[inputID] = applyLoadContents(val, cwlDir)
			}
		}
	}

	return merged
}

// resolveInputSecondaryFiles resolves secondary files for an input based on workflow declarations.
func resolveInputSecondaryFiles(val any, inputDef cwl.InputParam, cwlDir string) any {
	// Handle secondaryFiles at the input level.
	if len(inputDef.SecondaryFiles) > 0 {
		return secondaryfiles.ResolveForValue(val, inputDef.SecondaryFiles, cwlDir)
	}

	// Handle record types with field-level secondaryFiles.
	if len(inputDef.RecordFields) > 0 {
		recordVal, ok := val.(map[string]any)
		if !ok {
			return val
		}

		// Create a copy to avoid modifying the original.
		result := make(map[string]any)
		for k, v := range recordVal {
			result[k] = v
		}

		// Resolve secondaryFiles for each field.
		for _, field := range inputDef.RecordFields {
			if len(field.SecondaryFiles) == 0 {
				continue
			}
			if fieldVal, exists := result[field.Name]; exists && fieldVal != nil {
				result[field.Name] = secondaryfiles.ResolveForValue(fieldVal, field.SecondaryFiles, cwlDir)
			}
		}
		return result
	}

	return val
}

// Note: secondaryFiles resolution for tool inputs is now in internal/secondaryfiles package
// as secondaryfiles.ResolveForTool to be shared with the execution engine.

// Note: Input validation is now in internal/validate package as validate.ToolInputs
// to be shared with the execution engine.

// stageRenamedInputs creates symlinks for File inputs where basename differs from the
// actual filename. This happens when ExpressionTools modify the basename property.
// For each such file, a symlink is created in workDir with the new basename, and the
// path property is updated to point to the symlink.
func stageRenamedInputs(inputs map[string]any, workDir string) error {
	for _, v := range inputs {
		if err := stageRenamedValue(v, workDir); err != nil {
			return err
		}
	}
	return nil
}

// stageRenamedValue recursively processes a value, staging renamed files.
func stageRenamedValue(v any, workDir string) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return stageRenamedFileOrDirectory(val, workDir)
		}
		// Recursively handle nested maps (e.g., record fields).
		for _, nested := range val {
			if err := stageRenamedValue(nested, workDir); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range val {
			if err := stageRenamedValue(item, workDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageRenamedFileOrDirectory handles a single File or Directory with renamed basename.
// stageDir is the directory where symlinks should be created (workDir for primary files,
// primaryDirname for secondaryFiles).
func stageRenamedFileOrDirectory(obj map[string]any, stageDir string) error {
	basename, hasBasename := obj["basename"].(string)
	path, hasPath := obj["path"].(string)

	// Get the directory for staging secondary files.
	// For primary files, this is derived from dirname or path.
	// For secondary files, we use stageDir (the primary file's dirname).
	primaryDirname := stageDir
	if d, ok := obj["dirname"].(string); ok && d != "" {
		primaryDirname = d
	} else if hasPath && path != "" {
		primaryDirname = filepath.Dir(path)
	}

	// Process secondary files - they should be staged relative to this file's directory.
	if secFiles, ok := obj["secondaryFiles"].([]any); ok {
		for _, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				// Secondary files should be staged in the same directory as this file
				// (which is primaryDirname for this file).
				if err := stageRenamedFileOrDirectory(sfMap, primaryDirname); err != nil {
					return err
				}
			}
		}
	}

	// If no path or no basename, nothing to do for this file.
	if !hasPath || !hasBasename || path == "" || basename == "" {
		return nil
	}

	// Check if basename differs from the actual filename.
	actualBasename := filepath.Base(path)
	if basename == actualBasename {
		return nil // No rename needed
	}

	// Create symlink with the new basename in stageDir.
	linkPath := filepath.Join(stageDir, basename)

	// Check if link already exists.
	if _, err := os.Lstat(linkPath); err == nil {
		// Link exists - verify it points to the correct target.
		existingTarget, readErr := os.Readlink(linkPath)
		if readErr == nil && existingTarget == path {
			// Link exists and points to the correct target.
			obj["path"] = linkPath
			obj["dirname"] = stageDir
			return nil
		}
		// Link exists but points to wrong target - remove and recreate.
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove stale symlink %s: %w", linkPath, err)
		}
	}

	// Create the symlink.
	if err := os.Symlink(path, linkPath); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", linkPath, path, err)
	}

	// Update path to point to the staged symlink.
	obj["path"] = linkPath
	obj["dirname"] = stageDir

	return nil
}
