// Package cwlexpr provides a CWL expression evaluator using JavaScript (goja).
// It supports CWL parameter references, expressions, and JavaScript code blocks.
package cwlexpr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// Evaluator evaluates CWL expressions using a JavaScript runtime.
type Evaluator struct {
	vm            *goja.Runtime
	expressionLib []string
}

// NewEvaluator creates a new CWL expression evaluator.
// The expressionLib parameter contains JavaScript code to include before evaluation.
func NewEvaluator(expressionLib []string) *Evaluator {
	return &Evaluator{
		expressionLib: expressionLib,
	}
}

// setupVM creates and initializes a new JavaScript VM with the expression library.
func (e *Evaluator) setupVM(ctx *Context) (*goja.Runtime, error) {
	vm := goja.New()

	// Load expression library
	for i, lib := range e.expressionLib {
		if _, err := vm.RunString(lib); err != nil {
			return nil, fmt.Errorf("expressionLib[%d]: %w", i, err)
		}
	}

	// Set up context variables
	if err := vm.Set("inputs", ctx.Inputs); err != nil {
		return nil, fmt.Errorf("set inputs: %w", err)
	}
	if err := vm.Set("self", ctx.Self); err != nil {
		return nil, fmt.Errorf("set self: %w", err)
	}

	// Convert runtime struct to map for JavaScript access
	runtimeMap := map[string]any{
		"outdir":     ctx.Runtime.OutDir,
		"tmpdir":     ctx.Runtime.TmpDir,
		"cores":      ctx.Runtime.Cores,
		"ram":        ctx.Runtime.Ram,
		"outdirSize": ctx.Runtime.OutdirSize,
		"tmpdirSize": ctx.Runtime.TmpdirSize,
	}
	// Only include exitCode in outputEval context per CWL spec.
	if ctx.InOutputEval {
		runtimeMap["exitCode"] = ctx.Runtime.ExitCode
	}
	if err := vm.Set("runtime", runtimeMap); err != nil {
		return nil, fmt.Errorf("set runtime: %w", err)
	}

	return vm, nil
}

// Evaluate evaluates a CWL expression string with the given context.
// Supports three expression forms:
//   - Parameter references: $(inputs.file.basename)
//   - Simple expressions: $(inputs.count * 2)
//   - JavaScript code blocks: ${ return inputs.x + 1; }
//
// Returns the evaluated value.
func (e *Evaluator) Evaluate(expr string, ctx *Context) (any, error) {
	if expr == "" {
		return "", nil
	}

	// Check if this is a pure literal (no unescaped expressions)
	if !containsExpression(expr) {
		// Unescape \$( to $( per CWL spec.
		result := strings.ReplaceAll(expr, "\\$(", "$(")
		result = strings.ReplaceAll(result, "\\${", "${")
		return result, nil
	}

	vm, err := e.setupVM(ctx)
	if err != nil {
		return nil, err
	}

	// Check for JavaScript code block: ${ ... }
	// Trim only LEADING whitespace to handle YAML literal blocks.
	// Trailing content (including newlines from YAML | blocks) must be preserved.
	trimmedLeft := strings.TrimLeft(expr, " \t\n\r")
	if strings.HasPrefix(trimmedLeft, "${") {
		// Find the matching closing brace for the code block.
		if idx := findMatchingBrace(trimmedLeft); idx >= 0 {
			// Calculate how much leading whitespace was removed.
			leadingLen := len(expr) - len(trimmedLeft)
			// Get the remaining content after the code block from original expression.
			originalIdx := leadingLen + idx
			remaining := ""
			if originalIdx+1 < len(expr) {
				remaining = expr[originalIdx+1:]
			}

			// Trim trailing whitespace from remaining - YAML often adds trailing newlines.
			// Only consider it "content" if there's non-whitespace after the code block.
			remainingTrimmed := strings.TrimSpace(remaining)
			if remainingTrimmed == "" {
				// Sole code block — return typed result.
				return e.evaluateCodeBlock(vm, trimmedLeft[:idx+1])
			}
			// Code block followed by literal text (e.g., ${...}suffix).
			// Evaluate the code block, convert result to string, append rest.
			codeBlock := trimmedLeft[:idx+1]
			result, err := e.evaluateCodeBlock(vm, codeBlock)
			if err != nil {
				return nil, err
			}
			return toString(result) + remaining, nil
		}
	}

	// Handle string with embedded expressions: "prefix_$(inputs.id)_suffix"
	return e.evaluateInterpolated(vm, expr)
}

// evaluateCodeBlock evaluates a JavaScript code block: ${ ... }
func (e *Evaluator) evaluateCodeBlock(vm *goja.Runtime, expr string) (any, error) {
	// Extract the code between ${ and }
	code := strings.TrimPrefix(expr, "${")
	code = strings.TrimSuffix(code, "}")
	code = strings.TrimSpace(code)

	// Wrap in a function and execute
	wrapped := fmt.Sprintf("(function() { %s })()", code)
	val, err := vm.RunString(wrapped)
	if err != nil {
		return nil, fmt.Errorf("JavaScript error: %w", err)
	}

	return val.Export(), nil
}

// evaluateInterpolated evaluates a string with embedded $(expr) expressions.
func (e *Evaluator) evaluateInterpolated(vm *goja.Runtime, expr string) (any, error) {
	// Find all $(expr) patterns using balanced parenthesis matching
	matches := findExpressions(expr)

	if len(matches) == 0 {
		// No unescaped expressions found — unescape \$( to $( per CWL spec.
		result := strings.ReplaceAll(expr, "\\$(", "$(")
		result = strings.ReplaceAll(result, "\\${", "${")
		return result, nil
	}

	// If the entire string is a single expression, return the evaluated value directly
	if len(matches) == 1 && matches[0].start == 0 && matches[0].end == len(expr) {
		exprCode := matches[0].expr
		// If the expression starts with {, it's an object literal and needs parentheses.
		if strings.HasPrefix(strings.TrimSpace(exprCode), "{") {
			exprCode = "(" + exprCode + ")"
		}

		// CWL validation: Check .length access on non-array/non-string values.
		if err := validateLengthAccess(vm, exprCode); err != nil {
			return nil, err
		}

		val, err := vm.RunString(exprCode)
		if err != nil {
			return nil, fmt.Errorf("expression error in $(%s): %w", matches[0].expr, err)
		}
		// CWL validation: undefined values indicate invalid property access.
		if val == goja.Undefined() {
			return nil, fmt.Errorf("expression $(%s) returned undefined (invalid property access)", matches[0].expr)
		}
		return val.Export(), nil
	}

	// Build result string with interpolated values
	var result strings.Builder
	lastEnd := 0
	for _, match := range matches {
		// Append text before this expression
		result.WriteString(expr[lastEnd:match.start])

		// CWL validation: Check .length access on non-array/non-string values.
		if err := validateLengthAccess(vm, match.expr); err != nil {
			return nil, err
		}

		// Evaluate the expression
		val, err := vm.RunString(match.expr)
		if err != nil {
			return nil, fmt.Errorf("expression error in $(%s): %w", match.expr, err)
		}
		// CWL validation: undefined values indicate invalid property access.
		if val == goja.Undefined() {
			return nil, fmt.Errorf("expression $(%s) returned undefined (invalid property access)", match.expr)
		}

		// Convert to string and append
		result.WriteString(toString(val.Export()))
		lastEnd = match.end
	}
	// Append remaining text
	result.WriteString(expr[lastEnd:])

	// Unescape \$( to $( per CWL spec.
	out := strings.ReplaceAll(result.String(), "\\$(", "$(")
	out = strings.ReplaceAll(out, "\\${", "${")
	return out, nil
}

// findMatchingBrace finds the index of the closing brace for a ${...} code block.
// Returns -1 if no matching brace is found.
func findMatchingBrace(s string) int {
	if !strings.HasPrefix(s, "${") {
		return -1
	}
	depth := 0
	for i, c := range s {
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// exprMatch represents a matched CWL expression.
type exprMatch struct {
	start int    // start index of "$(" in the string
	end   int    // end index (after closing ")")
	expr  string // the expression content (without $( and ))
}

// findExpressions finds all $(expr) patterns in a string, handling nested parentheses.
func findExpressions(s string) []exprMatch {
	var matches []exprMatch
	i := 0
	for i < len(s)-1 {
		if s[i] == '$' && s[i+1] == '(' && (i == 0 || s[i-1] != '\\') {
			start := i
			// Find matching closing paren
			depth := 1
			j := i + 2
			for j < len(s) && depth > 0 {
				if s[j] == '(' {
					depth++
				} else if s[j] == ')' {
					depth--
				}
				j++
			}
			if depth == 0 {
				matches = append(matches, exprMatch{
					start: start,
					end:   j,
					expr:  s[start+2 : j-1],
				})
				i = j
				continue
			}
		}
		i++
	}
	return matches
}

// EvaluateBool evaluates an expression that should return a boolean.
func (e *Evaluator) EvaluateBool(expr string, ctx *Context) (bool, error) {
	val, err := e.Evaluate(expr, ctx)
	if err != nil {
		return false, err
	}

	switch v := val.(type) {
	case bool:
		return v, nil
	case nil:
		return false, nil
	default:
		return false, fmt.Errorf("expression did not return boolean: %T", val)
	}
}

// EvaluateString evaluates an expression that should return a string.
func (e *Evaluator) EvaluateString(expr string, ctx *Context) (string, error) {
	val, err := e.Evaluate(expr, ctx)
	if err != nil {
		return "", err
	}
	return toString(val), nil
}

// EvaluateInt evaluates an expression that should return an integer.
func (e *Evaluator) EvaluateInt(expr string, ctx *Context) (int64, error) {
	val, err := e.Evaluate(expr, ctx)
	if err != nil {
		return 0, err
	}

	switch v := val.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("expression did not return integer: %T", val)
	}
}

// validateLengthAccess validates that .length is only accessed on arrays, strings,
// or objects with a 'length' property. CWL spec requires accessing .length on
// other types to be an error, unlike JavaScript which silently returns undefined.
func validateLengthAccess(vm *goja.Runtime, exprCode string) error {
	// Check if expression ends with .length
	if !strings.HasSuffix(exprCode, ".length") {
		return nil
	}

	// Extract the base expression (everything before .length)
	baseExpr := strings.TrimSuffix(exprCode, ".length")
	if baseExpr == "" {
		return nil
	}

	// Evaluate the base expression to check its type
	baseVal, err := vm.RunString(baseExpr)
	if err != nil {
		// If base expression fails, let the full expression fail naturally
		return nil
	}

	exported := baseVal.Export()
	if exported == nil {
		return fmt.Errorf("cannot access .length on null value in expression: %s", exprCode)
	}

	// Check if the value supports .length (array, string, or object with length field)
	switch v := exported.(type) {
	case []any, string:
		// Valid - arrays and strings support .length
		return nil
	case map[string]any:
		// Valid if the map has a 'length' field (e.g., a CWL record with a length property)
		if _, hasLength := v["length"]; hasLength {
			return nil
		}
		return fmt.Errorf("cannot access .length on record without 'length' field in expression: %s", exprCode)
	default:
		return fmt.Errorf("cannot access .length on non-array value of type %T in expression: %s", exported, exprCode)
	}
}

// containsExpression checks if a string contains CWL expression syntax.
func containsExpression(s string) bool {
	if strings.HasPrefix(s, "${") {
		return true
	}
	// Check for unescaped $( — CWL spec says \$( is a literal $(, not an expression.
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '$' && s[i+1] == '(' {
			if i == 0 || s[i-1] != '\\' {
				return true
			}
		}
	}
	return false
}

// IsExpression returns true if the string is a CWL expression.
func IsExpression(s string) bool {
	return containsExpression(s)
}

// IsSoleExpression returns true if the string is a single CWL expression
// with no surrounding literal text. For example:
//   - "$(inputs.x)" → true
//   - "${return inputs.x}" → true
//   - "hello $(inputs.x)" → false (has prefix text)
//   - "$(inputs.x)\n" → false (has trailing text)
func IsSoleExpression(s string) bool {
	if strings.HasPrefix(s, "${") {
		// JS block expression — sole if it ends with } and is balanced.
		depth := 0
		for i, c := range s {
			if c == '{' {
				depth++
			} else if c == '}' {
				depth--
				if depth == 0 {
					return i == len(s)-1
				}
			}
		}
		return false
	}
	if strings.HasPrefix(s, "$(") {
		// Parameter reference — sole if balanced parens close at end.
		depth := 0
		for i, c := range s {
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					return i == len(s)-1
				}
			}
		}
		return false
	}
	return false
}

// toString converts any value to a string representation.
// Maps and arrays are converted to JSON format matching Python's json.dumps()
// default separators (", " and ": ").
// nil values become "null" (JSON representation).
// Floats are formatted without scientific notation.
func toString(v any) string {
	if v == nil {
		return "null"
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		// Format without scientific notation.
		return strconv.FormatFloat(val, 'f', -1, 64)
	case map[string]any, []any:
		return JsonDumps(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// JsonDumps serializes a value to JSON matching Python's json.dumps() default
// format: separators are ", " (comma-space) and ": " (colon-space).
func JsonDumps(v any) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		if val {
			return "true"
		}
		return "false"
	case string:
		data, _ := json.Marshal(val)
		return string(data)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	case json.Number:
		return string(val)
	case []any:
		parts := make([]string, len(val))
		for i, item := range val {
			parts[i] = JsonDumps(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			keyJSON, _ := json.Marshal(k)
			parts = append(parts, string(keyJSON)+": "+JsonDumps(val[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

// convertFloatsForJSON recursively converts float64 values to json.Number
// to avoid scientific notation in JSON output.
func convertFloatsForJSON(v any) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			result[k] = convertFloatsForJSON(v)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, v := range val {
			result[i] = convertFloatsForJSON(v)
		}
		return result
	case float64:
		// Format without scientific notation.
		return json.Number(strconv.FormatFloat(val, 'f', -1, 64))
	default:
		return v
	}
}
