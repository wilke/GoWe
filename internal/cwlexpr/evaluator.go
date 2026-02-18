// Package cwlexpr provides a CWL expression evaluator using JavaScript (goja).
// It supports CWL parameter references, expressions, and JavaScript code blocks.
package cwlexpr

import (
	"encoding/json"
	"fmt"
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

	// Check if this is a pure literal (no expressions)
	if !containsExpression(expr) {
		return expr, nil
	}

	vm, err := e.setupVM(ctx)
	if err != nil {
		return nil, err
	}

	// Check for JavaScript code block: ${ ... }
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		return e.evaluateCodeBlock(vm, expr)
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
		return expr, nil
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

	return result.String(), nil
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
		if s[i] == '$' && s[i+1] == '(' {
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
	return strings.Contains(s, "$(") || strings.HasPrefix(s, "${")
}

// IsExpression returns true if the string is a CWL expression.
func IsExpression(s string) bool {
	return containsExpression(s)
}

// toString converts any value to a string representation.
// Maps and arrays are converted to JSON format.
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
		// Convert floats to json.Number before marshaling to avoid scientific notation.
		converted := convertFloatsForJSON(val)
		jsonBytes, err := json.Marshal(converted)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", val)
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
