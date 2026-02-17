// Package cmdline builds command lines from CWL CommandLineTool definitions.
package cmdline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// Builder constructs command lines from CWL CommandLineTool definitions.
type Builder struct {
	evaluator *cwlexpr.Evaluator
}

// NewBuilder creates a new command line builder with the given expression library.
func NewBuilder(expressionLib []string) *Builder {
	return &Builder{
		evaluator: cwlexpr.NewEvaluator(expressionLib),
	}
}

// BuildResult contains the constructed command line and related information.
type BuildResult struct {
	// Command is the full command line as an array of strings.
	Command []string

	// Stdin is the file path for standard input (if specified).
	Stdin string

	// Stdout is the file path for standard output capture (if specified).
	Stdout string

	// Stderr is the file path for standard error capture (if specified).
	Stderr string
}

// Build constructs the command line for a CommandLineTool with the given inputs.
func (b *Builder) Build(tool *cwl.CommandLineTool, inputs map[string]any, runtime *cwlexpr.RuntimeContext) (*BuildResult, error) {
	var cmd []string

	// Start with baseCommand.
	baseCmd := normalizeBaseCommand(tool.BaseCommand)
	cmd = append(cmd, baseCmd...)

	// Collect all command line parts (arguments and input bindings).
	var parts []cmdPart

	// Add arguments.
	for i, arg := range tool.Arguments {
		part, err := b.buildArgument(arg, i, inputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("argument[%d]: %w", i, err)
		}
		if part != nil {
			parts = append(parts, *part)
		}
	}

	// Add input bindings.
	inputNames := sortedKeys(tool.Inputs)
	for _, name := range inputNames {
		input := tool.Inputs[name]
		if input.InputBinding == nil {
			continue
		}
		inputValue := inputs[name]
		part, err := b.buildInputBinding(name, &input, inputValue, inputs, runtime)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", name, err)
		}
		if part != nil {
			parts = append(parts, *part)
		}
	}

	// Sort parts by position, then by name (for stability).
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].position != parts[j].position {
			return parts[i].position < parts[j].position
		}
		return parts[i].name < parts[j].name
	})

	// Append sorted parts to command.
	for _, part := range parts {
		cmd = append(cmd, part.args...)
	}

	result := &BuildResult{Command: cmd}

	// Evaluate stdin/stdout/stderr expressions.
	ctx := cwlexpr.NewContext(inputs)
	if runtime != nil {
		ctx = ctx.WithRuntime(runtime)
	}

	if tool.Stdin != "" {
		stdin, err := b.evaluator.EvaluateString(tool.Stdin, ctx)
		if err != nil {
			return nil, fmt.Errorf("stdin: %w", err)
		}
		result.Stdin = stdin
	}

	if tool.Stdout != "" {
		stdout, err := b.evaluator.EvaluateString(tool.Stdout, ctx)
		if err != nil {
			return nil, fmt.Errorf("stdout: %w", err)
		}
		result.Stdout = stdout
	}

	if tool.Stderr != "" {
		stderr, err := b.evaluator.EvaluateString(tool.Stderr, ctx)
		if err != nil {
			return nil, fmt.Errorf("stderr: %w", err)
		}
		result.Stderr = stderr
	}

	return result, nil
}

// cmdPart represents a part of the command line with its position for sorting.
type cmdPart struct {
	position int
	name     string   // for stable sorting when positions are equal
	args     []string // the actual command-line arguments
}

// buildArgument builds a command-line part from a CWL argument.
func (b *Builder) buildArgument(arg any, index int, inputs map[string]any, runtime *cwlexpr.RuntimeContext) (*cmdPart, error) {
	ctx := cwlexpr.NewContext(inputs)
	if runtime != nil {
		ctx = ctx.WithRuntime(runtime)
	}

	switch a := arg.(type) {
	case string:
		// Simple string argument - may contain expressions.
		value, err := b.evaluator.EvaluateString(a, ctx)
		if err != nil {
			return nil, err
		}
		return &cmdPart{
			position: 0, // default position for string arguments
			name:     fmt.Sprintf("arg_%d", index),
			args:     []string{value},
		}, nil

	case cwl.Argument:
		// Structured argument.
		pos := 0
		if a.Position != nil {
			pos = *a.Position
		}

		// Evaluate valueFrom.
		value, err := b.evaluator.EvaluateString(a.ValueFrom, ctx)
		if err != nil {
			return nil, err
		}

		args := buildPrefixedArgs(a.Prefix, value, a.Separate)
		return &cmdPart{
			position: pos,
			name:     fmt.Sprintf("arg_%d", index),
			args:     args,
		}, nil
	}

	return nil, fmt.Errorf("unexpected argument type: %T", arg)
}

// buildInputBinding builds a command-line part from an input binding.
func (b *Builder) buildInputBinding(name string, input *cwl.ToolInputParam, value any, inputs map[string]any, runtime *cwlexpr.RuntimeContext) (*cmdPart, error) {
	binding := input.InputBinding
	if binding == nil {
		return nil, nil
	}

	// Skip null values.
	if value == nil {
		return nil, nil
	}

	ctx := cwlexpr.NewContext(inputs)
	if runtime != nil {
		ctx = ctx.WithRuntime(runtime)
	}
	ctx = ctx.WithSelf(value)

	pos := 0
	if binding.Position != nil {
		pos = *binding.Position
	}

	// Special handling for boolean values.
	if boolVal, ok := value.(bool); ok {
		if !boolVal {
			// False booleans are omitted entirely.
			return nil, nil
		}
		// True booleans with prefix: output just the prefix.
		if binding.Prefix != "" {
			return &cmdPart{
				position: pos,
				name:     name,
				args:     []string{binding.Prefix},
			}, nil
		}
		// True boolean without prefix and empty inputBinding: omit entirely.
		// CWL spec: "if inputBinding is empty, the boolean does not appear on command line"
		return nil, nil
	}

	// If valueFrom is set, it replaces the input value entirely.
	// This must be checked before array handling since valueFrom can replace an array with a scalar.
	if binding.ValueFrom != "" {
		evaluated, err := b.evaluator.Evaluate(binding.ValueFrom, ctx)
		if err != nil {
			return nil, err
		}
		strValue := valueToString(evaluated)
		if strValue == "" {
			return nil, nil
		}
		args := buildPrefixedArgs(binding.Prefix, strValue, binding.Separate)
		return &cmdPart{
			position: pos,
			name:     name,
			args:     args,
		}, nil
	}

	// Handle array values specially.
	if arrVal, ok := value.([]any); ok {
		return b.buildArrayInputBinding(name, input, arrVal, pos, ctx)
	}

	// Determine the value to use for non-array values.
	strValue := inputValueToString(value, binding.ItemSeparator)

	// Skip empty values.
	if strValue == "" {
		return nil, nil
	}

	args := buildPrefixedArgs(binding.Prefix, strValue, binding.Separate)
	return &cmdPart{
		position: pos,
		name:     name,
		args:     args,
	}, nil
}

// buildArrayInputBinding handles array input bindings per CWL spec.
// Supports both array-level binding (input.InputBinding) and item-level binding (input.ItemInputBinding).
func (b *Builder) buildArrayInputBinding(name string, input *cwl.ToolInputParam, values []any, pos int, ctx *cwlexpr.Context) (*cmdPart, error) {
	binding := input.InputBinding
	itemBinding := input.ItemInputBinding

	// If itemSeparator is set, join all values with that separator.
	if binding.ItemSeparator != "" {
		var items []string
		for _, item := range values {
			s := inputValueToString(item, "")
			if s != "" {
				items = append(items, s)
			}
		}
		if len(items) == 0 {
			return nil, nil
		}
		joined := strings.Join(items, binding.ItemSeparator)
		args := buildPrefixedArgs(binding.Prefix, joined, binding.Separate)
		return &cmdPart{
			position: pos,
			name:     name,
			args:     args,
		}, nil
	}

	// Build arguments for the array.
	var args []string

	// Array-level prefix appears once before all items.
	if binding.Prefix != "" {
		args = append(args, binding.Prefix)
	}

	// Each array element gets item-level binding (if present) or no prefix.
	for _, item := range values {
		s := inputValueToString(item, "")
		if s == "" {
			continue
		}
		if itemBinding != nil && itemBinding.Prefix != "" {
			// Item-level prefix for each element.
			itemArgs := buildPrefixedArgs(itemBinding.Prefix, s, itemBinding.Separate)
			args = append(args, itemArgs...)
		} else {
			// No item-level prefix.
			args = append(args, s)
		}
	}

	if len(args) == 0 {
		return nil, nil
	}

	return &cmdPart{
		position: pos,
		name:     name,
		args:     args,
	}, nil
}

// buildPrefixedArgs builds the argument array with optional prefix.
func buildPrefixedArgs(prefix, value string, separate *bool) []string {
	if prefix == "" {
		return []string{value}
	}

	// Default separate is true.
	sep := true
	if separate != nil {
		sep = *separate
	}

	if sep {
		return []string{prefix, value}
	}
	return []string{prefix + value}
}

// normalizeBaseCommand converts baseCommand to []string.
func normalizeBaseCommand(bc any) []string {
	switch cmd := bc.(type) {
	case string:
		return []string{cmd}
	case []string:
		return cmd
	case []any:
		var result []string
		for _, v := range cmd {
			if s, ok := v.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// inputValueToString converts an input value to a string for the command line.
func inputValueToString(value any, itemSeparator string) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "" // false booleans typically omit the argument entirely
	case int, int64, float64:
		return fmt.Sprintf("%v", v)
	case map[string]any:
		// File or Directory object - use path.
		if path, ok := v["path"].(string); ok {
			return path
		}
		if loc, ok := v["location"].(string); ok {
			return loc
		}
		return fmt.Sprintf("%v", v)
	case []any:
		// Array value - join with itemSeparator or space.
		sep := " "
		if itemSeparator != "" {
			sep = itemSeparator
		}
		var items []string
		for _, item := range v {
			s := inputValueToString(item, "")
			if s != "" {
				items = append(items, s)
			}
		}
		return strings.Join(items, sep)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// valueToString converts any value to a string.
func valueToString(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// sortedKeys returns the sorted keys of a map.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
