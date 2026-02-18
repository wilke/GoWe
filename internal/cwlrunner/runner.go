// Package cwlrunner provides a CWL v1.2 runner implementation.
package cwlrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/me/gowe/internal/cmdline"
	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	"gopkg.in/yaml.v3"
)

// Runner executes CWL tools and workflows.
type Runner struct {
	logger *slog.Logger
	parser *parser.Parser

	// Configuration options.
	OutDir       string
	NoContainer  bool
	ForceDocker  bool
	OutputFormat string // "json" or "yaml"
	ProcessID    string // specific process ID to run from $graph document

	// Internal state.
	cwlDir     string            // directory of CWL file, for resolving relative paths in defaults
	stepCount  int               // counter for unique step directories
	namespaces map[string]string // namespace prefix -> URI mappings
}

// NewRunner creates a new CWL runner.
func NewRunner(logger *slog.Logger) *Runner {
	return &Runner{
		logger:       logger,
		parser:       parser.New(logger),
		OutDir:       "./cwl-output",
		OutputFormat: "json",
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

	// If ProcessID is specified, select that specific process.
	if r.ProcessID != "" {
		// Normalize processID for comparison (strip leading #)
		processID := r.ProcessID
		processIDWithHash := "#" + r.ProcessID

		// Check if it's a tool.
		if tool, ok := graph.Tools[processID]; ok {
			outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
			if err != nil {
				return err
			}
			return r.writeOutputs(outputs, w)
		}
		if tool, ok := graph.Tools[processIDWithHash]; ok {
			outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
			if err != nil {
				return err
			}
			return r.writeOutputs(outputs, w)
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
		outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs, true)
		if err != nil {
			return err
		}
		return r.writeOutputs(outputs, w)
	}

	return fmt.Errorf("no tools found in document")
}

// executeTool executes a single CommandLineTool.
// If resolveSecondary is true, secondary files will be resolved from tool definitions.
// For workflow steps, secondary files should already be resolved from workflow inputs.
func (r *Runner) executeTool(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, inputs map[string]any, resolveSecondary bool) (map[string]any, error) {
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

	// Determine execution mode (Docker or local).
	useDocker := r.ForceDocker || (!r.NoContainer && hasDockerRequirement(tool))

	// Get the work directory for this execution (increments stepCount).
	r.stepCount++
	workDir := filepath.Join(r.OutDir, fmt.Sprintf("work_%d", r.stepCount))
	// Make workDir absolute for use in runtime.outdir expressions.
	if absWorkDir, err := filepath.Abs(workDir); err == nil {
		workDir = absWorkDir
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

	var outputs map[string]any
	if useDocker {
		dockerImage := getDockerImage(tool)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		outputs, err = r.executeInDockerWithWorkDir(ctx, tool, cmdResult, mergedInputs, dockerImage, workDir)
	} else {
		outputs, err = r.executeLocalWithWorkDir(ctx, tool, cmdResult, mergedInputs, workDir)
	}

	if err != nil {
		return nil, err
	}

	return outputs, nil
}

// executeExpressionTool executes a CWL ExpressionTool by evaluating its JavaScript expression.
func (r *Runner) executeExpressionTool(tool *cwl.ExpressionTool, inputs map[string]any, graph *cwl.GraphDocument) (map[string]any, error) {
	r.logger.Info("executing expression tool", "id", tool.ID)

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph)

	// Create expression context with inputs.
	ctx := cwlexpr.NewContext(inputs)
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
	r.logger.Info("executing workflow", "id", graph.Workflow.ID)

	// Merge workflow input defaults with provided inputs.
	mergedInputs := mergeWorkflowInputDefaults(graph.Workflow, inputs, r.cwlDir)

	// Build execution order using DAG.
	dag, err := parser.BuildDAG(graph.Workflow)
	if err != nil {
		return fmt.Errorf("build DAG: %w", err)
	}

	// Track outputs from completed steps.
	stepOutputs := make(map[string]map[string]any)

	// Execute steps in topological order.
	for _, stepID := range dag.Order {
		step := graph.Workflow.Steps[stepID]

		// Resolve step inputs.
		stepInputs := resolveStepInputs(step, mergedInputs, stepOutputs, r.cwlDir)

		// Check if this is an ExpressionTool.
		toolRef := stripHash(step.Run)
		if exprTool, ok := graph.ExpressionTools[toolRef]; ok {
			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				evaluator := cwlexpr.NewEvaluator(extractExpressionLib(graph))
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
		if len(step.Scatter) > 0 {
			outputs, err := r.executeScatter(ctx, graph, tool, step, stepInputs)
			if err != nil {
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		} else {
			// Handle conditional execution.
			if step.When != "" {
				evalCtx := cwlexpr.NewContext(stepInputs)
				evaluator := cwlexpr.NewEvaluator(extractExpressionLib(graph))
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

			outputs, err := r.executeTool(ctx, graph, tool, stepInputs, false)
			if err != nil {
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		}
	}

	// Collect workflow outputs (pass inputs for passthrough workflows).
	workflowOutputs := collectWorkflowOutputs(graph.Workflow, mergedInputs, stepOutputs)
	return r.writeOutputs(workflowOutputs, w)
}

// writeOutputs writes the outputs to the writer in the configured format.
func (r *Runner) writeOutputs(outputs map[string]any, w io.Writer) error {
	var data []byte
	var err error

	switch r.OutputFormat {
	case "yaml":
		data, err = yaml.Marshal(outputs)
	default:
		// Convert floats to json.Number to avoid scientific notation.
		converted := convertFloatsToNumbers(outputs)
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
// to avoid scientific notation in JSON output.
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

		// Get dependencies from DAG edges.
		for depID := range dag.Edges {
			if targets, ok := dag.Edges[depID]; ok {
				for _, target := range targets {
					if target == stepID {
						info.DependsOn = append(info.DependsOn, depID)
					}
				}
			}
		}

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
func resolveStepInputs(step cwl.Step, workflowInputs map[string]any, stepOutputs map[string]map[string]any, cwlDir string) map[string]any {
	resolved := make(map[string]any)
	for inputID, stepInput := range step.In {
		value := resolveSource(stepInput.Source, workflowInputs, stepOutputs)
		if value == nil && stepInput.Default != nil {
			// Resolve File/Directory objects in defaults relative to CWL directory.
			value = resolveDefaultValue(stepInput.Default, cwlDir)
		}
		resolved[inputID] = value
	}
	return resolved
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

// collectWorkflowOutputs collects outputs from completed steps or passthrough from inputs.
func collectWorkflowOutputs(wf *cwl.Workflow, workflowInputs map[string]any, stepOutputs map[string]map[string]any) map[string]any {
	outputs := make(map[string]any)
	for outputID, output := range wf.Outputs {
		outputs[outputID] = resolveSource(output.OutputSource, workflowInputs, stepOutputs)
	}
	return outputs
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

// hasDockerRequirement checks if a tool has a DockerRequirement.
func hasDockerRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints["DockerRequirement"]; ok {
			return true
		}
	}
	return false
}

// getDockerImage extracts the Docker image from requirements or hints.
func getDockerImage(tool *cwl.CommandLineTool) string {
	// Check requirements first.
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	// Then hints.
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
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
