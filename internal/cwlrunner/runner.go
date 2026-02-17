// Package cwlrunner provides a CWL v1.2 runner implementation.
package cwlrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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

	graph, err := r.parser.ParseGraph(data)
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
		builder := cmdline.NewBuilder(expressionLib)
		runtime := cwlexpr.DefaultRuntimeContext()
		runtime.OutDir = r.OutDir
		runtime.TmpDir = filepath.Join(r.OutDir, "tmp")

		result, err := builder.Build(tool, resolvedInputs, runtime)
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

	// Execute based on document type.
	if graph.OriginalClass == "Workflow" || len(graph.Tools) > 1 {
		return r.executeWorkflow(ctx, graph, resolvedInputs, w)
	}

	// Single tool execution.
	for _, tool := range graph.Tools {
		outputs, err := r.executeTool(ctx, graph, tool, resolvedInputs)
		if err != nil {
			return err
		}
		return r.writeOutputs(outputs, w)
	}

	return fmt.Errorf("no tools found in document")
}

// executeTool executes a single CommandLineTool.
func (r *Runner) executeTool(ctx context.Context, graph *cwl.GraphDocument, tool *cwl.CommandLineTool, inputs map[string]any) (map[string]any, error) {
	r.logger.Info("executing tool", "id", tool.ID)

	// Get expression library from requirements.
	expressionLib := extractExpressionLib(graph)

	// Build command line.
	builder := cmdline.NewBuilder(expressionLib)
	runtime := cwlexpr.DefaultRuntimeContext()
	runtime.OutDir = r.OutDir
	runtime.TmpDir = filepath.Join(r.OutDir, "tmp")

	cmdResult, err := builder.Build(tool, inputs, runtime)
	if err != nil {
		return nil, fmt.Errorf("build command: %w", err)
	}

	r.logger.Debug("built command", "cmd", cmdResult.Command)

	// Determine execution mode (Docker or local).
	useDocker := r.ForceDocker || (!r.NoContainer && hasDockerRequirement(tool))

	var outputs map[string]any
	if useDocker {
		dockerImage := getDockerImage(tool)
		if dockerImage == "" {
			return nil, fmt.Errorf("Docker execution requested but no docker image specified")
		}
		outputs, err = r.executeInDocker(ctx, tool, cmdResult, inputs, dockerImage)
	} else {
		outputs, err = r.executeLocal(ctx, tool, cmdResult, inputs)
	}

	if err != nil {
		return nil, err
	}

	return outputs, nil
}

// executeWorkflow executes a workflow.
func (r *Runner) executeWorkflow(ctx context.Context, graph *cwl.GraphDocument, inputs map[string]any, w io.Writer) error {
	r.logger.Info("executing workflow", "id", graph.Workflow.ID)

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
		tool := graph.Tools[stripHash(step.Run)]
		if tool == nil {
			return fmt.Errorf("step %s: tool %s not found", stepID, step.Run)
		}

		// Resolve step inputs.
		stepInputs := resolveStepInputs(step, inputs, stepOutputs)

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

			outputs, err := r.executeTool(ctx, graph, tool, stepInputs)
			if err != nil {
				return fmt.Errorf("step %s: %w", stepID, err)
			}
			stepOutputs[stepID] = outputs
		}
	}

	// Collect workflow outputs.
	workflowOutputs := collectWorkflowOutputs(graph.Workflow, stepOutputs)
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
		data, err = json.MarshalIndent(outputs, "", "  ")
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

	// Resolve path or location.
	if path, ok := resolved["path"].(string); ok {
		if !filepath.IsAbs(path) {
			resolved["path"] = filepath.Join(baseDir, path)
		}
	}
	if loc, ok := resolved["location"].(string); ok {
		if !filepath.IsAbs(loc) && !isURI(loc) {
			resolved["location"] = filepath.Join(baseDir, loc)
		}
	}

	return resolved
}

// isURI checks if a string is a URI.
func isURI(s string) bool {
	return len(s) > 5 && (s[:5] == "file:" || s[:5] == "http:" || s[:6] == "https:")
}

// resolveStepInputs resolves inputs for a workflow step.
func resolveStepInputs(step cwl.Step, workflowInputs map[string]any, stepOutputs map[string]map[string]any) map[string]any {
	resolved := make(map[string]any)
	for inputID, stepInput := range step.In {
		value := resolveSource(stepInput.Source, workflowInputs, stepOutputs)
		if value == nil && stepInput.Default != nil {
			value = stepInput.Default
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

// collectWorkflowOutputs collects outputs from completed steps.
func collectWorkflowOutputs(wf *cwl.Workflow, stepOutputs map[string]map[string]any) map[string]any {
	outputs := make(map[string]any)
	for outputID, output := range wf.Outputs {
		outputs[outputID] = resolveSource(output.OutputSource, nil, stepOutputs)
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

// stripHash removes the leading "#" from a tool reference.
func stripHash(ref string) string {
	if len(ref) > 0 && ref[0] == '#' {
		return ref[1:]
	}
	return ref
}
