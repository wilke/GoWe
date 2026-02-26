package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// LocalExecutor runs tasks as local OS processes.
type LocalExecutor struct {
	logger  *slog.Logger
	workDir string
}

// NewLocalExecutor creates a LocalExecutor rooted at workDir.
// If workDir is empty, os.TempDir() is used.
func NewLocalExecutor(workDir string, logger *slog.Logger) *LocalExecutor {
	if workDir == "" {
		workDir = os.TempDir()
	}
	return &LocalExecutor{
		workDir: workDir,
		logger:  logger.With("component", "local-executor"),
	}
}

// Type returns model.ExecutorTypeLocal.
func (e *LocalExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeLocal
}

// Submit executes the task synchronously as a local process.
// It returns the task working directory as the externalID.
func (e *LocalExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	taskDir := filepath.Join(e.workDir, task.ID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		return "", fmt.Errorf("task %s: create work dir: %w", task.ID, err)
	}

	// Use execution.Engine for full CWL support when Tool/Job are available.
	if task.HasTool() {
		return e.submitWithEngine(ctx, task, taskDir)
	}

	// Legacy path: use _base_command from task.Inputs.
	return e.submitLegacy(ctx, task, taskDir)
}

// submitWithEngine executes a task using execution.Engine with full CWL output support.
func (e *LocalExecutor) submitWithEngine(ctx context.Context, task *model.Task, taskDir string) (string, error) {
	e.logger.Debug("executing with engine", "task_id", task.ID)

	// Parse tool from task.Tool map.
	tool, err := parseToolFromMap(task.Tool)
	if err != nil {
		return "", fmt.Errorf("task %s: parse tool: %w", task.ID, err)
	}

	// Build engine configuration.
	var expressionLib []string
	var namespaces map[string]string
	var cwlDir string
	if task.RuntimeHints != nil {
		expressionLib = task.RuntimeHints.ExpressionLib
		namespaces = task.RuntimeHints.Namespaces
		cwlDir = task.RuntimeHints.CWLDir
	}

	engine := execution.NewEngine(execution.Config{
		Logger:        e.logger,
		ExpressionLib: expressionLib,
		Namespaces:    namespaces,
		CWLDir:        cwlDir,
	})

	// Execute the tool.
	result, err := engine.ExecuteTool(ctx, tool, task.Job, taskDir)
	if err != nil {
		// Even on error, capture any partial results.
		if result != nil {
			task.ExitCode = &result.ExitCode
			task.Stdout = result.Stdout
			task.Stderr = result.Stderr
			task.Outputs = result.Outputs
		}
		return taskDir, fmt.Errorf("task %s: execute: %w", task.ID, err)
	}

	// Capture results.
	task.ExitCode = &result.ExitCode
	task.Stdout = result.Stdout
	task.Stderr = result.Stderr
	task.Outputs = result.Outputs

	e.logger.Debug("task completed with engine",
		"task_id", task.ID,
		"exit_code", result.ExitCode,
		"outputs", len(result.Outputs),
	)

	return taskDir, nil
}

// submitLegacy executes a task using the legacy _base_command approach.
func (e *LocalExecutor) submitLegacy(ctx context.Context, task *model.Task, taskDir string) (string, error) {
	parts := extractBaseCommand(task.Inputs)
	if len(parts) == 0 {
		return "", fmt.Errorf("task %s: _base_command is missing or empty", task.ID)
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = taskDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	task.Stdout = stdoutBuf.String()
	task.Stderr = stderrBuf.String()

	var exitCode int
	switch err := runErr.(type) {
	case nil:
		exitCode = 0
	case *exec.ExitError:
		exitCode = err.ExitCode()
	default:
		// Non-exit errors (e.g. binary not found) are returned directly.
		return "", fmt.Errorf("task %s: run command: %w", task.ID, runErr)
	}
	task.ExitCode = &exitCode

	// Collect outputs via glob patterns.
	if globs, ok := task.Inputs["_output_globs"].(map[string]any); ok {
		if task.Outputs == nil {
			task.Outputs = make(map[string]any)
		}
		for outputID, raw := range globs {
			pattern, ok := raw.(string)
			if !ok {
				continue
			}
			matches, err := filepath.Glob(filepath.Join(taskDir, pattern))
			if err != nil {
				continue
			}
			if len(matches) == 1 {
				task.Outputs[outputID] = matches[0]
			} else if len(matches) > 1 {
				task.Outputs[outputID] = matches
			}
		}
	}

	e.logger.Debug("task submitted (legacy)",
		"task_id", task.ID,
		"command", parts,
		"exit_code", exitCode,
	)

	return taskDir, nil
}

// Status derives the task state from the recorded exit code.
// If no exit code has been set yet the task is considered queued.
// When the task has a Tool with successCodes defined, those are checked.
func (e *LocalExecutor) Status(_ context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExitCode == nil {
		return model.TaskStateQueued, nil
	}

	// Check against tool's successCodes if available.
	if task.Tool != nil {
		if sc, ok := task.Tool["successCodes"].([]any); ok && len(sc) > 0 {
			// Check if exit code is in successCodes.
			for _, code := range sc {
				switch c := code.(type) {
				case int:
					if *task.ExitCode == c {
						return model.TaskStateSuccess, nil
					}
				case float64:
					if *task.ExitCode == int(c) {
						return model.TaskStateSuccess, nil
					}
				}
			}
			// Exit code not in successCodes - check permanentFailCodes and temporaryFailCodes.
			if pf, ok := task.Tool["permanentFailCodes"].([]any); ok {
				for _, code := range pf {
					switch c := code.(type) {
					case int:
						if *task.ExitCode == c {
							return model.TaskStateFailed, nil
						}
					case float64:
						if *task.ExitCode == int(c) {
							return model.TaskStateFailed, nil
						}
					}
				}
			}
			// If not in successCodes or permanentFailCodes, it's a temporary failure (will retry).
			return model.TaskStateFailed, nil
		}
	}

	// Default behavior: exit code 0 is success, anything else is failure.
	if *task.ExitCode == 0 {
		return model.TaskStateSuccess, nil
	}
	return model.TaskStateFailed, nil
}

// Cancel is a no-op for LocalExecutor; context cancellation handles termination.
func (e *LocalExecutor) Cancel(_ context.Context, _ *model.Task) error {
	return nil
}

// Logs returns the captured stdout and stderr stored on the task.
func (e *LocalExecutor) Logs(_ context.Context, task *model.Task) (string, string, error) {
	return task.Stdout, task.Stderr, nil
}

// extractBaseCommand reads the _base_command key from task inputs and converts
// its []any elements to a []string.
func extractBaseCommand(inputs map[string]any) []string {
	raw, ok := inputs["_base_command"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// parseToolFromMap converts a task.Tool map to a cwl.CommandLineTool.
// With proper JSON tags on cwl types, this is now a simple JSON roundtrip.
func parseToolFromMap(toolMap map[string]any) (*cwl.CommandLineTool, error) {
	// Marshal to JSON then unmarshal to struct.
	// The cwl.CommandLineTool has proper json tags, so this preserves all fields.
	data, err := json.Marshal(toolMap)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}

	var tool cwl.CommandLineTool
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("unmarshal tool: %w", err)
	}

	// Handle baseCommand which may be string or []string in YAML.
	if bc, ok := toolMap["baseCommand"]; ok {
		switch cmd := bc.(type) {
		case string:
			tool.BaseCommand = []string{cmd}
		case []any:
			var baseCmd []string
			for _, c := range cmd {
				if s, ok := c.(string); ok {
					baseCmd = append(baseCmd, s)
				}
			}
			tool.BaseCommand = baseCmd
		case []string:
			tool.BaseCommand = cmd
		}
	}

	// Handle arguments which need custom parsing for ArgumentEntry.
	if argsRaw, ok := toolMap["arguments"].([]any); ok {
		tool.Arguments = parseArguments(argsRaw)
	}

	// Inputs and outputs are handled by JSON unmarshal via cwl struct tags.
	// Only initialize empty maps if nil to avoid nil pointer issues.
	if tool.Inputs == nil {
		tool.Inputs = make(map[string]cwl.ToolInputParam)
	}
	if tool.Outputs == nil {
		tool.Outputs = make(map[string]cwl.ToolOutputParam)
	}

	return &tool, nil
}

// parseInputParam converts an input parameter map to cwl.ToolInputParam.
func parseInputParam(param any) cwl.ToolInputParam {
	var result cwl.ToolInputParam
	switch p := param.(type) {
	case string:
		result.Type = p
	case map[string]any:
		if t, ok := p["type"].(string); ok {
			result.Type = t
		} else if typeMap, ok := p["type"].(map[string]any); ok {
			// Handle complex types (array, record, union).
			if typeStr, ok := typeMap["type"].(string); ok {
				switch typeStr {
				case "array":
					if items, ok := typeMap["items"].(string); ok {
						result.Type = items + "[]"
					}
					// Parse itemInputBinding from nested array type.
					if itemIB, ok := typeMap["inputBinding"].(map[string]any); ok {
						result.ItemInputBinding = parseInputBinding(itemIB)
					}
				case "record":
					result.Type = "record"
					// Parse record fields with their inputBindings.
					if fields, ok := typeMap["fields"].([]any); ok {
						result.RecordFields = parseRecordFields(fields)
					}
				default:
					result.Type = typeStr
				}
			} else {
				result.Type = fmt.Sprintf("%v", p["type"])
			}
		}
		if d, ok := p["default"]; ok {
			result.Default = d
		}
		if ib, ok := p["inputBinding"].(map[string]any); ok {
			result.InputBinding = parseInputBinding(ib)
		}
		// Parse itemInputBinding if stored at top level (from JSON).
		if itemIB, ok := p["itemInputBinding"].(map[string]any); ok {
			result.ItemInputBinding = parseInputBinding(itemIB)
		}
		// Parse recordFields if stored at top level (from JSON).
		if rf, ok := p["recordFields"].([]any); ok {
			result.RecordFields = parseRecordFields(rf)
		}
	}
	return result
}

// parseOutputParam converts an output parameter map to cwl.ToolOutputParam.
func parseOutputParam(param any) cwl.ToolOutputParam {
	var result cwl.ToolOutputParam
	switch p := param.(type) {
	case string:
		result.Type = p
	case map[string]any:
		if t, ok := p["type"].(string); ok {
			result.Type = t
		} else if t, ok := p["type"]; ok {
			// Handle complex types.
			result.Type = fmt.Sprintf("%v", t)
		}
		if ob, ok := p["outputBinding"].(map[string]any); ok {
			result.OutputBinding = parseOutputBinding(ob)
		}
	}
	return result
}

// parseInputBinding converts an inputBinding map to cwl.InputBinding.
func parseInputBinding(m map[string]any) *cwl.InputBinding {
	ib := &cwl.InputBinding{}
	// Position can be int or float64 depending on JSON source.
	if pos, ok := m["position"].(float64); ok {
		ib.Position = int(pos)
	} else if pos, ok := m["position"].(int); ok {
		ib.Position = pos
	}
	if prefix, ok := m["prefix"].(string); ok {
		ib.Prefix = prefix
	}
	if sep, ok := m["separate"].(bool); ok {
		ib.Separate = &sep
	}
	if vf, ok := m["valueFrom"].(string); ok {
		ib.ValueFrom = vf
	}
	if is, ok := m["itemSeparator"].(string); ok {
		ib.ItemSeparator = is
	}
	return ib
}

// parseOutputBinding converts an outputBinding map to cwl.OutputBinding.
func parseOutputBinding(m map[string]any) *cwl.OutputBinding {
	ob := &cwl.OutputBinding{}
	if glob, ok := m["glob"].(string); ok {
		ob.Glob = glob
	}
	if lc, ok := m["loadContents"].(bool); ok {
		ob.LoadContents = lc
	}
	if oe, ok := m["outputEval"].(string); ok {
		ob.OutputEval = oe
	}
	return ob
}

// parseArguments converts an arguments array to []cwl.ArgumentEntry.
func parseArguments(args []any) []cwl.ArgumentEntry {
	result := make([]cwl.ArgumentEntry, 0, len(args))
	for _, arg := range args {
		switch a := arg.(type) {
		case string:
			result = append(result, cwl.ArgumentEntry{
				StringValue: a,
				IsString:    true,
			})
		case map[string]any:
			binding := parseArgumentBinding(a)
			result = append(result, cwl.ArgumentEntry{
				Binding:  binding,
				IsString: false,
			})
		}
	}
	return result
}

// parseArgumentBinding converts an argument binding map to cwl.Argument.
func parseArgumentBinding(m map[string]any) *cwl.Argument {
	arg := &cwl.Argument{}

	// Position - check both keys and handle int/float types.
	if pos, ok := m["position"]; ok {
		arg.Position = normalizePosition(pos)
	} else if pos, ok := m["Position"]; ok {
		arg.Position = normalizePosition(pos)
	}

	// Prefix.
	if prefix, ok := m["prefix"].(string); ok {
		arg.Prefix = prefix
	} else if prefix, ok := m["Prefix"].(string); ok {
		arg.Prefix = prefix
	}

	// ValueFrom.
	if vf, ok := m["valueFrom"].(string); ok {
		arg.ValueFrom = vf
	} else if vf, ok := m["ValueFrom"].(string); ok {
		arg.ValueFrom = vf
	}

	// Separate.
	if sep, ok := m["separate"].(bool); ok {
		arg.Separate = &sep
	} else if sep, ok := m["Separate"].(bool); ok {
		arg.Separate = &sep
	}

	// ShellQuote.
	if sq, ok := m["shellQuote"].(bool); ok {
		arg.ShellQuote = &sq
	} else if sq, ok := m["ShellQuote"].(bool); ok {
		arg.ShellQuote = &sq
	}

	return arg
}

// normalizePosition converts position values to int for consistency.
func normalizePosition(pos any) any {
	switch p := pos.(type) {
	case float64:
		return int(p)
	case int64:
		return int(p)
	default:
		return p
	}
}

// parseIntArray converts []any to []int for exit code arrays.
func parseIntArray(arr []any) []int {
	result := make([]int, 0, len(arr))
	for _, v := range arr {
		switch n := v.(type) {
		case int:
			result = append(result, n)
		case int64:
			result = append(result, int(n))
		case float64:
			result = append(result, int(n))
		}
	}
	return result
}

// parseRecordFields parses record field definitions for input bindings.
func parseRecordFields(fields []any) []cwl.RecordField {
	var result []cwl.RecordField
	for _, item := range fields {
		if fieldMap, ok := item.(map[string]any); ok {
			field := cwl.RecordField{}
			if name, ok := fieldMap["name"].(string); ok {
				field.Name = name
			}
			if t, ok := fieldMap["type"].(string); ok {
				field.Type = t
			}
			if doc, ok := fieldMap["doc"].(string); ok {
				field.Doc = doc
			}
			if ib, ok := fieldMap["inputBinding"].(map[string]any); ok {
				field.InputBinding = parseInputBinding(ib)
			}
			// Parse secondaryFiles for record fields.
			if sf, ok := fieldMap["secondaryFiles"].([]any); ok {
				field.SecondaryFiles = parseSecondaryFiles(sf)
			}
			result = append(result, field)
		}
	}
	return result
}

// parseSecondaryFiles parses secondaryFiles from JSON array.
func parseSecondaryFiles(items []any) []cwl.SecondaryFileSchema {
	var result []cwl.SecondaryFileSchema
	for _, item := range items {
		switch v := item.(type) {
		case string:
			// Simple string pattern.
			result = append(result, cwl.SecondaryFileSchema{Pattern: v})
		case map[string]any:
			// Full schema with pattern and required fields.
			schema := cwl.SecondaryFileSchema{}
			if pattern, ok := v["pattern"].(string); ok {
				schema.Pattern = pattern
			}
			if req, ok := v["required"]; ok {
				schema.Required = req
			}
			result = append(result, schema)
		}
	}
	return result
}
