// Package cwlrunner provides a CWL v1.2 runner implementation.
package cwlrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/cwloutput"
	"github.com/me/gowe/internal/parser"
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
	cwlDir     string            // directory of CWL file, for resolving relative paths in defaults
	stepCount  int               // counter for unique step directories
	stepMu     sync.Mutex        // protects stepCount for parallel execution
	namespaces map[string]string // namespace prefix -> URI mappings
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
func (r *Runner) LoadInputs(jobPath string) (map[string]any, error) {
	if jobPath == "" {
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
	expressionLib := extractExpressionLib(graph)

	// Build command line for each tool.
	for toolID, tool := range graph.Tools {
		// Merge tool defaults with resolved inputs.
		mergedInputs, err := mergeToolDefaults(tool, resolvedInputs, r.cwlDir)
		if err != nil {
			return fmt.Errorf("process inputs for %s: %w", toolID, err)
		}

		// Build runtime context from tool requirements.
		runtime := buildRuntimeContext(tool, r.OutDir)

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
	if graph.OriginalClass == "Workflow" || len(graph.Tools) > 1 {
		return r.executeWorkflow(ctx, graph, resolvedInputs, w)
	}

	// Single tool execution.
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

	// Resolve secondaryFiles for tool inputs if requested (direct tool execution).
	resolvedInputs := inputs
	if resolveSecondary {
		resolvedInputs = resolveToolSecondaryFiles(tool, inputs, r.cwlDir)
	}

	// Merge tool input defaults with resolved inputs.
	mergedInputs, err := mergeToolDefaults(tool, resolvedInputs, r.cwlDir)
	if err != nil {
		return nil, fmt.Errorf("process inputs: %w", err)
	}

	// Validate inputs against tool schema.
	if err := validateToolInputs(tool, mergedInputs); err != nil {
		return nil, err
	}

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph)

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
	if err := stageInitialWorkDir(tool, mergedInputs, workDir, expressionLib, r.cwlDir); err != nil {
		return nil, fmt.Errorf("stage InitialWorkDirRequirement: %w", err)
	}

	// Build runtime context using the actual work directory.
	runtime := buildRuntimeContext(tool, workDir)

	// Build command line.
	builder := cmdline.NewBuilder(expressionLib)
	cmdResult, err := builder.Build(tool, mergedInputs, runtime)
	if err != nil {
		return nil, fmt.Errorf("build command: %w", err)
	}

	r.logger.Debug("built command", "cmd", cmdResult.Command)

	var result *ExecutionResult
	switch containerRuntime {
	case "docker":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		result, err = r.executeInDockerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir)
	case "apptainer":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Apptainer execution requested but no docker image specified")
		}
		result, err = r.executeInApptainerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir)
	default:
		result, err = r.executeLocalWithWorkDir(ctx, tool, cmdResult, mergedInputs, workDir)
	}

	if err != nil {
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
		return nil, err
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
	if err := validateToolInputs(tool, mergedInputs); err != nil {
		return nil, err
	}

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph)

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

	// Build runtime context.
	runtime := buildRuntimeContext(tool, workDir)

	// Build command line.
	builder := cmdline.NewBuilder(expressionLib)
	cmdResult, err := builder.Build(tool, mergedInputs, runtime)
	if err != nil {
		return nil, fmt.Errorf("build command: %w", err)
	}

	// Execute based on container runtime
	switch containerRuntime {
	case "docker":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		return r.executeInDockerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir)
	case "apptainer":
		dockerImage := getDockerImage(tool, graph.Workflow)
		if dockerImage == "" {
			return nil, fmt.Errorf("Apptainer execution requested but no docker image specified")
		}
		return r.executeInApptainerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir)
	default:
		return r.executeLocalWithWorkDir(ctx, tool, cmdResult, mergedInputs, workDir)
	}
}

// executeExpressionTool executes a CWL ExpressionTool by evaluating its JavaScript expression.
func (r *Runner) executeExpressionTool(tool *cwl.ExpressionTool, inputs map[string]any, graph *cwl.GraphDocument) (map[string]any, error) {
	r.logger.Info("executing expression tool", "id", tool.ID)

	// Apply loadContents for inputs that have it enabled.
	processedInputs := make(map[string]any)
	for inputID, val := range inputs {
		processedInputs[inputID] = val
	}
	for inputID, inputDef := range tool.Inputs {
		if inputDef.LoadContents {
			if val, exists := processedInputs[inputID]; exists && val != nil {
				processedInputs[inputID] = applyLoadContents(val, r.cwlDir)
			}
		}
	}

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph)

	// Create expression context with processed inputs (with contents loaded).
	ctx := cwlexpr.NewContext(processedInputs)
	evaluator := cwlexpr.NewEvaluator(expressionLib)

	// Evaluate the expression.
	result, err := evaluator.Evaluate(tool.Expression, ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate expression: %w", err)
	}

	// The expression should return an object with output field names.
	outputs, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expression did not return an object, got %T", result)
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
	evaluator := cwlexpr.NewEvaluator(extractExpressionLib(graph))

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

		// Otherwise it's a CommandLineTool.
		tool := graph.Tools[toolRef]
		if tool == nil {
			return fmt.Errorf("step %s: tool %s not found", stepID, step.Run)
		}

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
		converted := convertFloatsToNumbers(outputWithMetrics)
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

// convertFloatsToNumbers recursively converts float64 values to json.Number
// to avoid scientific notation in JSON output. NaN and Inf values are converted
// to null since JSON does not support these special float values.
func convertFloatsToNumbers(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			result[k] = convertFloatsToNumbers(v)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, v := range val {
			result[i] = convertFloatsToNumbers(v)
		}
		return result
	case float64:
		// NaN and Inf are not valid JSON - convert to null.
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil
		}
		// Format without scientific notation.
		return json.Number(strconv.FormatFloat(val, 'f', -1, 64))
	default:
		return v
	}
}

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
	if contents, hasContents := resolved["contents"].(string); hasContents {
		_, hasPath := resolved["path"]
		_, hasLocation := resolved["location"]
		if !hasPath && !hasLocation {
			// File literal - materialize to temp file.
			tempFile, err := materializeFileLiteral(contents, resolved)
			if err == nil {
				resolved["path"] = tempFile
				resolved["location"] = "file://" + tempFile
			}
		}
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
				if decoded, err := url.PathUnescape(path); err == nil {
					path = decoded
				}
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

// materializeFileLiteral creates a temp file from file literal contents.
// Per CWL spec, file literals with "contents" field are written to a temp file.
func materializeFileLiteral(contents string, fileObj map[string]any) (string, error) {
	// Use basename if provided, otherwise generate a name.
	basename := "cwl_literal"
	if b, ok := fileObj["basename"].(string); ok && b != "" {
		basename = b
	}

	// Create temp directory for file literals.
	// Resolve symlinks (e.g., /var -> /private/var on macOS) for Docker compatibility.
	tempDir := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = resolved
	}
	tempDir = filepath.Join(tempDir, "cwl-literals")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", err
	}

	// Create the temp file.
	tempPath := filepath.Join(tempDir, basename)
	if err := os.WriteFile(tempPath, []byte(contents), 0644); err != nil {
		return "", err
	}

	return tempPath, nil
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
			value = values
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

// stageInitialWorkDir stages files from InitialWorkDirRequirement into the work directory.
// Supports the full CWL v1.2 spec: Dirent entries with entryname/entry/writable,
// File/Directory objects in listing, expression entries, arrays, and null handling.
func stageInitialWorkDir(tool *cwl.CommandLineTool, inputs map[string]any, workDir string, expressionLib []string, cwlDir string) error {
	reqRaw, ok := tool.Requirements["InitialWorkDirRequirement"]
	if !ok {
		return nil
	}

	reqMap, ok := reqRaw.(map[string]any)
	if !ok {
		return nil
	}

	listingRaw, ok := reqMap["listing"]
	if !ok {
		return nil
	}

	evaluator := cwlexpr.NewEvaluator(expressionLib)

	// stagedPaths maps original absolute path → staged absolute path for entryname renames.
	stagedPaths := make(map[string]string)

	// The listing can itself be an expression (e.g., "$(inputs.indir.listing)").
	listing, err := resolveIWDListing(listingRaw, inputs, evaluator)
	if err != nil {
		return err
	}

	for _, item := range listing {
		if item == nil {
			continue
		}
		if err := stageIWDItem(item, inputs, workDir, evaluator, cwlDir, stagedPaths); err != nil {
			return err
		}
	}

	// Update input File paths to reflect staged locations.
	updateInputPathsForIWD(inputs, workDir, stagedPaths)

	return nil
}

// resolveIWDListing resolves the listing field which can be an array or an expression.
func resolveIWDListing(listingRaw any, inputs map[string]any, evaluator *cwlexpr.Evaluator) ([]any, error) {
	switch v := listingRaw.(type) {
	case []any:
		return v, nil
	case string:
		// listing itself is an expression.
		if cwlexpr.IsExpression(v) {
			ctx := cwlexpr.NewContext(inputs)
			result, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return nil, fmt.Errorf("evaluate listing expression: %w", err)
			}
			if arr, ok := result.([]any); ok {
				return arr, nil
			}
			// If result is a string that looks like JSON array (from YAML | blocks
			// which append trailing newline causing stringification), try to parse it.
			if str, ok := result.(string); ok {
				str = strings.TrimSpace(str)
				if strings.HasPrefix(str, "[") {
					var arr []any
					if err := json.Unmarshal([]byte(str), &arr); err == nil {
						return arr, nil
					}
				}
			}
			if result == nil {
				return nil, nil
			}
			// Single item.
			return []any{result}, nil
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// stageIWDItem stages a single item from the InitialWorkDirRequirement listing.
// An item can be: a Dirent (map with entry/entryname), a File/Directory object,
// a string expression that evaluates to File/Directory/array, or null.
func stageIWDItem(item any, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, cwlDir string, stagedPaths map[string]string) error {
	switch v := item.(type) {
	case map[string]any:
		// Check if this is a File or Directory object (has "class" field).
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				resolveIWDObjectPaths(v, cwlDir)
				return stageIWDFile(v, "", false, workDir, stagedPaths)
			case "Directory":
				resolveIWDObjectPaths(v, cwlDir)
				return stageIWDDirectory(v, "", false, workDir, stagedPaths)
			}
		}
		// Otherwise treat as Dirent.
		return stageIWDDirent(v, inputs, workDir, evaluator, cwlDir, stagedPaths)

	case []any:
		// An array in listing — flatten and stage each item.
		for _, sub := range v {
			if sub == nil {
				continue
			}
			if err := stageIWDItem(sub, inputs, workDir, evaluator, cwlDir, stagedPaths); err != nil {
				return err
			}
		}
		return nil

	case string:
		// A bare expression in listing (e.g., "$(inputs.input_file)").
		if cwlexpr.IsExpression(v) {
			ctx := cwlexpr.NewContext(inputs)
			result, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return fmt.Errorf("evaluate listing item: %w", err)
			}
			return stageIWDEvaluatedResult(result, "", false, workDir, inputs, evaluator, stagedPaths)
		}
		return nil

	default:
		return nil
	}
}

// resolveIWDObjectPaths resolves relative location/path fields in File/Directory
// objects found directly in InitialWorkDirRequirement listing, relative to the CWL file directory.
func resolveIWDObjectPaths(obj map[string]any, cwlDir string) {
	if cwlDir == "" {
		return
	}
	pathResolved := false
	if loc, ok := obj["location"].(string); ok && loc != "" && !filepath.IsAbs(loc) && !isURI(loc) {
		absLoc := filepath.Clean(filepath.Join(cwlDir, loc))
		obj["location"] = absLoc
		if _, hasPath := obj["path"]; !hasPath {
			obj["path"] = absLoc
			pathResolved = true
		}
	}
	if !pathResolved {
		if p, ok := obj["path"].(string); ok && p != "" && !filepath.IsAbs(p) {
			obj["path"] = filepath.Clean(filepath.Join(cwlDir, p))
		}
	}
	// Resolve listing entries recursively.
	if listing, ok := obj["listing"].([]any); ok {
		for _, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				resolveIWDObjectPaths(itemMap, cwlDir)
			}
		}
	}
}

// stageIWDDirent stages a Dirent entry (map with entry, optional entryname and writable).
func stageIWDDirent(dirent map[string]any, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, cwlDir string, stagedPaths map[string]string) error {
	entryname, _ := dirent["entryname"].(string)
	entryRaw := dirent["entry"]
	writable, _ := dirent["writable"].(bool)

	// entry can be nil (e.g., $(null)).
	if entryRaw == nil {
		return nil
	}

	// Validate entryname: must not contain path traversal (../).
	if entryname != "" {
		cleaned := filepath.Clean(entryname)
		if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
			return fmt.Errorf("entryname %q is invalid: must not reference parent directory", entryname)
		}
	}

	// Evaluate entryname if it's an expression.
	if entryname != "" && cwlexpr.IsExpression(entryname) {
		ctx := cwlexpr.NewContext(inputs)
		evaluated, err := evaluator.Evaluate(entryname, ctx)
		if err != nil {
			return fmt.Errorf("evaluate entryname %q: %w", entryname, err)
		}
		entryname = fmt.Sprintf("%v", evaluated)
	}

	// Evaluate entry.
	switch v := entryRaw.(type) {
	case string:
		return stageIWDStringEntry(v, entryname, writable, inputs, workDir, evaluator, stagedPaths)
	case map[string]any:
		// File/Directory object literal.
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				return stageIWDFile(v, entryname, writable, workDir, stagedPaths)
			case "Directory":
				return stageIWDDirectory(v, entryname, writable, workDir, stagedPaths)
			}
		}
		return nil
	default:
		return nil
	}
}

// stageIWDStringEntry handles a Dirent entry that is a string (literal or expression).
func stageIWDStringEntry(entry, entryname string, writable bool, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, stagedPaths map[string]string) error {
	if !cwlexpr.IsExpression(entry) {
		// Pure literal string content — unescape \$( to $(.
		content := strings.ReplaceAll(entry, "\\$(", "$(")
		content = strings.ReplaceAll(content, "\\${", "${")
		if entryname == "" {
			return nil
		}
		return writeIWDFile(workDir, entryname, content)
	}

	// Check if the entire string is a single expression (no surrounding text).
	// If so, the result type determines behavior (File object vs string content).
	// Important: check on the original entry, not trimmed — YAML | adds trailing \n
	// which makes it NOT a sole expression (content should include the trailing text).
	isSoleExpr := cwlexpr.IsSoleExpression(entry)

	ctx := cwlexpr.NewContext(inputs)
	evaluated, err := evaluator.Evaluate(entry, ctx)
	if err != nil {
		return fmt.Errorf("evaluate entry for %q: %w", entryname, err)
	}

	if isSoleExpr {
		// Single expression — result could be File, Directory, array, string, number, etc.
		return stageIWDEvaluatedResult(evaluated, entryname, writable, workDir, inputs, evaluator, stagedPaths)
	}

	// String interpolation — result is always string content.
	content := fmt.Sprintf("%v", evaluated)
	if entryname == "" {
		return nil
	}
	return writeIWDFile(workDir, entryname, content)
}

// stageIWDEvaluatedResult stages the result of evaluating an expression.
// The result can be a File, Directory, array, string, number, null, etc.
func stageIWDEvaluatedResult(result any, entryname string, writable bool, workDir string, inputs map[string]any, evaluator *cwlexpr.Evaluator, stagedPaths map[string]string) error {
	if result == nil {
		return nil
	}

	switch v := result.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				return stageIWDFile(v, entryname, writable, workDir, stagedPaths)
			case "Directory":
				return stageIWDDirectory(v, entryname, writable, workDir, stagedPaths)
			}
		}
		// Object that isn't File/Directory — serialize to JSON.
		if entryname != "" {
			return writeIWDFile(workDir, entryname, iwdResultToString(v))
		}
		return nil

	case []any:
		// Could be an array of File/Directory objects or a JSON array to serialize.
		if len(v) > 0 {
			if first, ok := v[0].(map[string]any); ok {
				if class, ok := first["class"].(string); ok && (class == "File" || class == "Directory") {
					// Array of File/Directory objects — stage each.
					for _, item := range v {
						if err := stageIWDEvaluatedResult(item, "", writable, workDir, inputs, evaluator, stagedPaths); err != nil {
							return err
						}
					}
					return nil
				}
			}
		}
		// JSON array — serialize.
		if entryname != "" {
			return writeIWDFile(workDir, entryname, iwdResultToString(v))
		}
		return nil

	case string:
		if entryname != "" {
			return writeIWDFile(workDir, entryname, v)
		}
		return nil

	default:
		// Number, bool, etc.
		if entryname != "" {
			return writeIWDFile(workDir, entryname, iwdResultToString(v))
		}
		return nil
	}
}

// stageIWDFile stages a CWL File object into the work directory.
func stageIWDFile(fileObj map[string]any, entryname string, writable bool, workDir string, stagedPaths map[string]string) error {
	// Get source path.
	srcPath := ""
	if p, ok := fileObj["path"].(string); ok {
		srcPath = p
	} else if loc, ok := fileObj["location"].(string); ok {
		srcPath = strings.TrimPrefix(loc, "file://")
	}

	if srcPath == "" {
		return nil
	}

	// Determine destination name.
	destName := entryname
	if destName == "" {
		if bn, ok := fileObj["basename"].(string); ok {
			destName = bn
		} else {
			destName = filepath.Base(srcPath)
		}
	}

	destPath := filepath.Join(workDir, destName)

	// Create parent directory if needed (for nested paths).
	if dir := filepath.Dir(destPath); dir != workDir {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create parent dir for %q: %w", destName, err)
		}
	}

	// Record the staging for input path updates.
	if stagedPaths != nil && entryname != "" {
		absSrc, _ := filepath.Abs(srcPath)
		stagedPaths[absSrc] = destPath
	}

	if writable {
		// Also stage secondaryFiles if present.
		if err := copyFile(srcPath, destPath); err != nil {
			return err
		}
		stageIWDSecondaryFiles(fileObj, workDir, destName, writable, stagedPaths)
		return nil
	}
	// Non-writable: symlink.
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		absSrc = srcPath
	}
	if err := os.Symlink(absSrc, destPath); err != nil {
		return err
	}
	stageIWDSecondaryFiles(fileObj, workDir, destName, writable, stagedPaths)
	return nil
}

// stageIWDSecondaryFiles stages secondaryFiles alongside a staged file.
func stageIWDSecondaryFiles(fileObj map[string]any, workDir, destName string, writable bool, stagedPaths map[string]string) {
	secFiles, ok := fileObj["secondaryFiles"].([]any)
	if !ok {
		return
	}
	for _, sf := range secFiles {
		sfObj, ok := sf.(map[string]any)
		if !ok {
			continue
		}
		sfPath := ""
		if p, ok := sfObj["path"].(string); ok {
			sfPath = p
		} else if loc, ok := sfObj["location"].(string); ok {
			sfPath = strings.TrimPrefix(loc, "file://")
		}
		if sfPath == "" {
			continue
		}
		sfBasename := filepath.Base(sfPath)
		sfDest := filepath.Join(workDir, sfBasename)
		// If the primary file was renamed with entryname, don't rename secondaryFiles.
		if writable {
			_ = copyFile(sfPath, sfDest)
		} else {
			absSf, _ := filepath.Abs(sfPath)
			_ = os.Symlink(absSf, sfDest)
		}
	}
}

// stageIWDDirectory stages a CWL Directory object into the work directory.
func stageIWDDirectory(dirObj map[string]any, entryname string, writable bool, workDir string, stagedPaths map[string]string) error {
	// Get source path.
	srcPath := ""
	if p, ok := dirObj["path"].(string); ok {
		srcPath = p
	} else if loc, ok := dirObj["location"].(string); ok {
		srcPath = strings.TrimPrefix(loc, "file://")
	}

	// Determine destination name.
	destName := entryname
	if destName == "" {
		if bn, ok := dirObj["basename"].(string); ok {
			destName = bn
		} else if srcPath != "" {
			destName = filepath.Base(srcPath)
		}
	}

	destPath := filepath.Join(workDir, destName)

	// Handle Directory with listing but no source path (synthetic directory).
	if srcPath == "" {
		if err := os.MkdirAll(destPath, 0755); err != nil {
			return fmt.Errorf("create directory %q: %w", destName, err)
		}
		// Stage listing contents if present.
		if listing, ok := dirObj["listing"].([]any); ok {
			for _, item := range listing {
				if fileObj, ok := item.(map[string]any); ok {
					if class, _ := fileObj["class"].(string); class == "File" {
						if err := stageIWDFile(fileObj, "", writable, destPath, stagedPaths); err != nil {
							return err
						}
					} else if class == "Directory" {
						if err := stageIWDDirectory(fileObj, "", writable, destPath, stagedPaths); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}

	if writable {
		return copyDir(srcPath, destPath)
	}

	// Non-writable: symlink.
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		absSrc = srcPath
	}
	return os.Symlink(absSrc, destPath)
}

// writeIWDFile writes content to a file in the work directory.
func writeIWDFile(workDir, name, content string) error {
	outPath := filepath.Join(workDir, name)
	// Create parent directory if needed.
	if dir := filepath.Dir(outPath); dir != workDir {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create parent dir for %q: %w", name, err)
		}
	}
	return os.WriteFile(outPath, []byte(content), 0644)
}

// iwdResultToString converts a value to its string representation for file content.
// Per CWL spec: objects and arrays are JSON-serialized (matching Python json.dumps format),
// numbers/booleans are stringified.
func iwdResultToString(v any) string {
	return cwlexpr.JsonDumps(v)
}

// copyFile copies a file from src to dst with write permissions.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return os.WriteFile(dst, data, 0644)
}

// copyDir recursively copies a directory tree with write permissions.
func copyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()|0755); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// updateInputPathsForIWD updates input File objects' paths to reflect staged locations.
// Per CWL spec, when a File is staged (possibly with entryname), inputs.file.path should
// point to the new location in the working directory.
// stagedPaths maps original absolute path → staged absolute path for entryname renames.
func updateInputPathsForIWD(inputs map[string]any, workDir string, stagedPaths map[string]string) {
	for _, v := range inputs {
		updateInputPathValue(v, workDir, stagedPaths)
	}
}

// updateInputPathValue recursively updates paths for staged files.
func updateInputPathValue(v any, workDir string, stagedPaths map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			if origPath, ok := val["path"].(string); ok {
				// Check if this file was explicitly staged with entryname.
				if newPath, ok := stagedPaths[origPath]; ok {
					val["path"] = newPath
					val["basename"] = filepath.Base(newPath)
					return
				}
				// Otherwise check if staged by basename.
				bn := filepath.Base(origPath)
				stagedPath := filepath.Join(workDir, bn)
				if _, err := os.Lstat(stagedPath); err == nil {
					val["path"] = stagedPath
				}
			}
		}
		if class, ok := val["class"].(string); ok && class == "Directory" {
			if origPath, ok := val["path"].(string); ok {
				if newPath, ok := stagedPaths[origPath]; ok {
					val["path"] = newPath
					val["basename"] = filepath.Base(newPath)
				}
			}
		}
	case []any:
		for _, item := range val {
			updateInputPathValue(item, workDir, stagedPaths)
		}
	}
}

// populateDirectoryListings adds listing entries to Directory inputs based on loadListing.
func populateDirectoryListings(tool *cwl.CommandLineTool, inputs map[string]any) {
	for inputID, inp := range tool.Inputs {
		loadListing := inp.LoadListing
		if loadListing == "" || loadListing == "no_listing" {
			continue
		}

		inputVal, ok := inputs[inputID]
		if !ok || inputVal == nil {
			continue
		}

		if dirObj, ok := inputVal.(map[string]any); ok {
			if class, _ := dirObj["class"].(string); class == "Directory" {
				populateDirListing(dirObj, loadListing)
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
func extractExpressionLib(graph *cwl.GraphDocument) []string {
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
						if s, ok := item.(string); ok {
							result = append(result, s)
						}
					}
					return result
				}
			}
		}
	}

	return nil
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

// buildRuntimeContext creates a RuntimeContext from tool requirements.
func buildRuntimeContext(tool *cwl.CommandLineTool, outDir string) *cwlexpr.RuntimeContext {
	runtime := cwlexpr.DefaultRuntimeContext()
	runtime.OutDir = outDir
	runtime.TmpDir = filepath.Join(outDir, "tmp")

	// Apply ResourceRequirement if present.
	rr := getResourceRequirement(tool)
	if rr != nil {
		// CWL allows coresMin/coresMax - use coresMin if present.
		if coresMin, ok := rr["coresMin"]; ok {
			switch v := coresMin.(type) {
			case int:
				runtime.Cores = v
			case float64:
				runtime.Cores = int(v)
			}
		}
		// If no coresMin, try cores.
		if runtime.Cores == 1 {
			if cores, ok := rr["cores"]; ok {
				switch v := cores.(type) {
				case int:
					runtime.Cores = v
				case float64:
					runtime.Cores = int(v)
				}
			}
		}

		// Apply RAM requirements.
		if ramMin, ok := rr["ramMin"]; ok {
			switch v := ramMin.(type) {
			case int:
				runtime.Ram = int64(v)
			case int64:
				runtime.Ram = v
			case float64:
				runtime.Ram = int64(v)
			}
		}
	}

	return runtime
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
		if v, exists := inputs[inputID]; exists {
			val = v
		} else if inputDef.Default != nil {
			// Use default value if input not provided.
			val = resolveDefaultValue(inputDef.Default, cwlDir)
		}

		// Process loadContents for File inputs.
		if val != nil && inputDef.LoadContents {
			processedVal, err := processLoadContents(val, cwlDir)
			if err != nil {
				return nil, fmt.Errorf("input %q: %w", inputID, err)
			}
			val = processedVal
		}

		// Validate secondaryFiles requirements.
		if val != nil {
			if err := validateSecondaryFiles(inputID, inputDef, val); err != nil {
				return nil, err
			}
		}

		merged[inputID] = val
	}

	return merged, nil
}

// validateSecondaryFiles checks that required secondary files are present in the input.
func validateSecondaryFiles(inputID string, inputDef cwl.ToolInputParam, val any) error {
	// Check if input parameter has secondaryFiles requirements.
	if len(inputDef.SecondaryFiles) > 0 {
		if err := checkFileHasSecondaryFiles(inputID, val, inputDef.SecondaryFiles); err != nil {
			return err
		}
	}

	// Check if record fields have secondaryFiles requirements.
	if len(inputDef.RecordFields) > 0 {
		recordVal, ok := val.(map[string]any)
		if !ok {
			return nil // Not a record value, nothing to validate.
		}

		for _, field := range inputDef.RecordFields {
			if len(field.SecondaryFiles) == 0 {
				continue
			}

			fieldVal, exists := recordVal[field.Name]
			if !exists || fieldVal == nil {
				continue
			}

			fieldPath := inputID + "." + field.Name
			if err := checkFileHasSecondaryFiles(fieldPath, fieldVal, field.SecondaryFiles); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkFileHasSecondaryFiles validates that a File value has the required secondary files.
func checkFileHasSecondaryFiles(path string, val any, required []cwl.SecondaryFileSchema) error {
	switch v := val.(type) {
	case map[string]any:
		// Single File object.
		if class, ok := v["class"].(string); ok && class == "File" {
			return validateFileSecondaryFiles(path, v, required)
		}
		return nil

	case []any:
		// Array of Files.
		for i, item := range v {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			if err := checkFileHasSecondaryFiles(itemPath, item, required); err != nil {
				return err
			}
		}
		return nil

	default:
		return nil
	}
}

// validateFileSecondaryFiles checks that a single File object has the required secondary files.
func validateFileSecondaryFiles(path string, fileObj map[string]any, required []cwl.SecondaryFileSchema) error {
	// Get the list of secondary files attached to this file.
	existingSecondary := make(map[string]bool)
	if secFiles, ok := fileObj["secondaryFiles"].([]any); ok {
		for _, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				if loc, ok := sfMap["location"].(string); ok {
					existingSecondary[filepath.Base(loc)] = true
				} else if p, ok := sfMap["path"].(string); ok {
					existingSecondary[filepath.Base(p)] = true
				}
			}
		}
	}

	// Get the basename of the primary file.
	var basename string
	if b, ok := fileObj["basename"].(string); ok {
		basename = b
	} else if loc, ok := fileObj["location"].(string); ok {
		basename = filepath.Base(loc)
	} else if p, ok := fileObj["path"].(string); ok {
		basename = filepath.Base(p)
	}

	// Check each required secondary file.
	for _, schema := range required {
		// Skip if required is explicitly false.
		if req, ok := schema.Required.(bool); ok && !req {
			continue
		}

		// Compute the expected secondary file name.
		expectedName := computeSecondaryFileName(basename, schema.Pattern)

		if !existingSecondary[expectedName] {
			return fmt.Errorf("input %q: missing required secondary file %q (pattern: %s)", path, expectedName, schema.Pattern)
		}
	}

	return nil
}

// computeSecondaryFileName computes the secondary file name from a base name and pattern.
func computeSecondaryFileName(basename, pattern string) string {
	// Handle caret pattern (replace extension).
	if strings.HasPrefix(pattern, "^") {
		// Count carets and remove that many extensions.
		carets := 0
		for strings.HasPrefix(pattern[carets:], "^") {
			carets++
		}
		suffix := pattern[carets:]

		// Remove extensions.
		name := basename
		for i := 0; i < carets; i++ {
			ext := filepath.Ext(name)
			if ext == "" {
				break
			}
			name = name[:len(name)-len(ext)]
		}
		return name + suffix
	}

	// Simple suffix pattern.
	return basename + pattern
}

// processLoadContents loads file contents into a File object (with 64KB limit).
func processLoadContents(val any, cwlDir string) (any, error) {
	const maxLoadContentsSize = 64 * 1024 // 64KB

	switch v := val.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			// Get the file path.
			path := ""
			if p, ok := v["path"].(string); ok {
				path = p
			} else if loc, ok := v["location"].(string); ok {
				path = strings.TrimPrefix(loc, "file://")
			}
			if path == "" {
				return nil, fmt.Errorf("File object has no path or location")
			}

			// Resolve relative paths.
			if !filepath.IsAbs(path) {
				path = filepath.Join(cwlDir, path)
			}

			// Check file size.
			info, err := os.Stat(path)
			if err != nil {
				return nil, fmt.Errorf("stat file: %w", err)
			}
			if info.Size() > maxLoadContentsSize {
				return nil, fmt.Errorf("loadContents: file %q is %d bytes, exceeds 64KB limit", path, info.Size())
			}

			// Read contents.
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read file contents: %w", err)
			}

			// Create a copy of the map with contents added.
			result := make(map[string]any)
			for k, val := range v {
				result[k] = val
			}
			result["contents"] = string(content)
			return result, nil
		}
		return val, nil

	case []any:
		// Process array of Files.
		result := make([]any, len(v))
		for i, item := range v {
			processed, err := processLoadContents(item, cwlDir)
			if err != nil {
				return nil, err
			}
			result[i] = processed
		}
		return result, nil

	default:
		return val, nil
	}
}

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
		return resolveSecondaryFilesForValue(val, inputDef.SecondaryFiles, cwlDir)
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
				result[field.Name] = resolveSecondaryFilesForValue(fieldVal, field.SecondaryFiles, cwlDir)
			}
		}
		return result
	}

	return val
}

// resolveSecondaryFilesForValue resolves secondary files for a File or array of Files.
func resolveSecondaryFilesForValue(val any, schemas []cwl.SecondaryFileSchema, cwlDir string) any {
	switch v := val.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			return resolveSecondaryFilesForFile(v, schemas, cwlDir)
		}
		return v

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = resolveSecondaryFilesForValue(item, schemas, cwlDir)
		}
		return result

	default:
		return val
	}
}

// resolveSecondaryFilesForFile adds secondary files to a File object based on patterns.
func resolveSecondaryFilesForFile(fileObj map[string]any, schemas []cwl.SecondaryFileSchema, cwlDir string) map[string]any {
	// Create a copy to avoid modifying the original.
	result := make(map[string]any)
	for k, v := range fileObj {
		result[k] = v
	}

	// Get the file's path or location.
	var filePath string
	if p, ok := result["path"].(string); ok {
		filePath = p
	} else if loc, ok := result["location"].(string); ok {
		filePath = strings.TrimPrefix(loc, "file://")
	}
	if filePath == "" {
		return result
	}

	// Resolve relative paths.
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwlDir, filePath)
	}

	// Get existing secondary files (if any).
	var secondaryFiles []any
	if existing, ok := result["secondaryFiles"].([]any); ok {
		secondaryFiles = existing
	}

	// Add secondary files based on patterns.
	basename := filepath.Base(filePath)
	dir := filepath.Dir(filePath)

	for _, schema := range schemas {
		secFileName := computeSecondaryFileName(basename, schema.Pattern)
		secPath := filepath.Join(dir, secFileName)

		// Check if the secondary file exists.
		if _, err := os.Stat(secPath); err != nil {
			// File doesn't exist - skip (validation will catch this later if required).
			continue
		}

		// Create the secondary file object.
		secFileObj := map[string]any{
			"class":    "File",
			"path":     secPath,
			"basename": secFileName,
			"location": "file://" + secPath,
		}

		// Add file metadata.
		if info, err := os.Stat(secPath); err == nil {
			secFileObj["size"] = info.Size()
		}

		secondaryFiles = append(secondaryFiles, secFileObj)
	}

	if len(secondaryFiles) > 0 {
		result["secondaryFiles"] = secondaryFiles
	}

	return result
}

// resolveToolSecondaryFiles resolves secondary files for tool inputs based on tool declarations.
func resolveToolSecondaryFiles(tool *cwl.CommandLineTool, inputs map[string]any, cwlDir string) map[string]any {
	result := make(map[string]any)

	// Copy all inputs first.
	for k, v := range inputs {
		result[k] = v
	}

	// Resolve secondaryFiles for each input based on tool's input definitions.
	for inputID, inputDef := range tool.Inputs {
		val, exists := result[inputID]
		if !exists || val == nil {
			continue
		}

		// Handle secondaryFiles at the input level.
		if len(inputDef.SecondaryFiles) > 0 {
			result[inputID] = resolveSecondaryFilesForValue(val, inputDef.SecondaryFiles, cwlDir)
			continue
		}

		// Handle record types with field-level secondaryFiles.
		if len(inputDef.RecordFields) > 0 {
			recordVal, ok := val.(map[string]any)
			if !ok {
				continue
			}

			// Create a copy to avoid modifying the original.
			resolvedRecord := make(map[string]any)
			for k, v := range recordVal {
				resolvedRecord[k] = v
			}

			// Resolve secondaryFiles for each field.
			for _, field := range inputDef.RecordFields {
				if len(field.SecondaryFiles) == 0 {
					continue
				}
				if fieldVal, exists := resolvedRecord[field.Name]; exists && fieldVal != nil {
					resolvedRecord[field.Name] = resolveSecondaryFilesForValue(fieldVal, field.SecondaryFiles, cwlDir)
				}
			}
			result[inputID] = resolvedRecord
		}
	}

	return result
}

// validateToolInputs validates that inputs match the tool's input schema.
// Returns an error if required inputs are missing or null is provided for non-optional types.
func validateToolInputs(tool *cwl.CommandLineTool, inputs map[string]any) error {
	for inputID, inputDef := range tool.Inputs {
		value, exists := inputs[inputID]

		// Check if input is optional (type ends with ? or is a union with null).
		isOptional := isOptionalType(inputDef.Type)

		// Check for missing required inputs.
		if !exists {
			if inputDef.Default == nil && !isOptional {
				return fmt.Errorf("missing required input: %s", inputID)
			}
			continue
		}

		// Check for null values on non-optional inputs.
		if value == nil && !isOptional {
			return fmt.Errorf("null is not valid for non-optional input: %s (type: %s)", inputID, inputDef.Type)
		}
	}
	return nil
}

// isOptionalType checks if a CWL type is optional (can be null).
// Types ending with ? or types that are unions including null are optional.
func isOptionalType(t string) bool {
	if t == "" {
		return false
	}
	// Type ending with ? is optional.
	if strings.HasSuffix(t, "?") {
		return true
	}
	// "null" type itself is optional.
	if t == "null" {
		return true
	}
	return false
}
