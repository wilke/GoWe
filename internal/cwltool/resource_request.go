package cwltool

import (
	"fmt"
	"math"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/pkg/cwl"
)

// ResourceRequest is the cores / RAM a tool asks for, as the server can know
// it at dispatch time. Zero means "not declared" or "not evaluable
// server-side"; callers treat zero as no constraint (#324).
type ResourceRequest struct {
	Cores int
	RamMB int64
}

// EvaluateResourceRequest computes the coresMin/cores and ramMin a worker's
// ResolveResources would enforce, so the scheduler can gate checkout on them.
//
// It mirrors the worker semantics (ResourceRequirement from requirements
// before hints; coresMin, then the non-standard `cores` key when coresMin is
// absent or evaluates to 1; floats round up; strings are evaluated only when
// they start with "$(" or "${") but, unlike the worker, reports failures
// instead of defaulting to 1. A failing dimension is returned as 0 and err
// describes the first failure; the other dimension is kept.
//
// The job is merged with scalar input defaults only: File defaults are not
// resolved and loadContents is not applied, so an expression that depends on
// file size or contents (File.size is unknown here) fails and yields 0.
// When expressionLib is nil the tool's own InlineJavascriptRequirement
// library is used.
func EvaluateResourceRequest(tool *cwl.CommandLineTool, job map[string]any, expressionLib []string) (ResourceRequest, error) {
	var req ResourceRequest
	if tool == nil {
		return req, nil
	}
	rr := getResourceRequirement(tool)
	if rr == nil {
		return req, nil
	}
	if expressionLib == nil {
		expressionLib = ExtractExpressionLibFromTool(tool)
	}

	var evaluator *cwlexpr.Evaluator
	var ctx *cwlexpr.Context
	// The JS VM is only needed for expressions; static values skip it.
	lazy := func() (*cwlexpr.Evaluator, *cwlexpr.Context) {
		if evaluator == nil {
			evaluator = cwlexpr.NewEvaluator(expressionLib)
			// Deep copy: PopulateDerivedFileProperties mutates File maps and
			// the job belongs to the task.
			inputs := deepCopyMap(mergeScalarDefaults(tool, job))
			PopulateDerivedFileProperties(inputs)
			ctx = cwlexpr.NewContext(inputs)
			ctx.Runtime = cwlexpr.DefaultRuntimeContext()
		}
		return evaluator, ctx
	}
	eval := func(v any) (int, error) {
		if s, ok := v.(string); ok && (strings.HasPrefix(s, "$(") || strings.HasPrefix(s, "${")) {
			e, c := lazy()
			return evalResourceIntStrict(v, e, c)
		}
		return evalResourceIntStrict(v, nil, nil)
	}

	var firstErr error
	note := func(name string, err error) {
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", name, err)
		}
	}

	coresOK := false
	if v, ok := rr["coresMin"]; ok {
		n, err := eval(v)
		if err != nil {
			note("coresMin", err)
		} else {
			req.Cores, coresOK = n, true
		}
	}
	// Worker rule: `cores` applies when coresMin is absent or resolved to 1.
	if !coresOK || req.Cores == 1 {
		if v, ok := rr["cores"]; ok {
			n, err := eval(v)
			if err != nil {
				note("cores", err)
			} else {
				req.Cores = n
			}
		}
	}
	if v, ok := rr["ramMin"]; ok {
		n, err := eval(v)
		if err != nil {
			note("ramMin", err)
		} else {
			req.RamMB = int64(n)
		}
	}
	return req, firstErr
}

// evalResourceIntStrict converts a ResourceRequirement value to a positive
// int, returning an error for anything it cannot evaluate.
func evalResourceIntStrict(value any, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) (int, error) {
	n, err := evalResourceIntRaw(value, evaluator, ctx)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("non-positive value %d", n)
	}
	return n, nil
}

// evalResourceIntRaw is evalResourceIntStrict without the positivity check,
// so the worker's legacy evalResourceInt keeps accepting 0 and negatives.
func evalResourceIntRaw(value any, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) (int, error) {
	var n int
	switch v := value.(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("non-finite number %v", v)
		}
		n = int(math.Ceil(v))
	case string:
		if evaluator == nil || ctx == nil || !(strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "${")) {
			return 0, fmt.Errorf("%q is not an evaluable expression", v)
		}
		result, err := evaluator.Evaluate(v, ctx)
		if err != nil {
			return 0, fmt.Errorf("evaluate %q: %w", v, err)
		}
		switch r := result.(type) {
		case int, int64, float64:
			return evalResourceIntRaw(r, nil, nil)
		default:
			return 0, fmt.Errorf("expression %q returned %T, want a number", v, result)
		}
	default:
		return 0, fmt.Errorf("unsupported resource value type %T", value)
	}
	return n, nil
}

// mergeScalarDefaults copies job and fills inputs that are missing or null
// with the tool's raw declared default. Unlike MergeToolDefaults it does no
// File resolution and no loadContents, so it is cheap and cannot fail.
func mergeScalarDefaults(tool *cwl.CommandLineTool, job map[string]any) map[string]any {
	merged := make(map[string]any, len(job)+len(tool.Inputs))
	for k, v := range job {
		merged[k] = v
	}
	for id, def := range tool.Inputs {
		if def.Default != nil && merged[id] == nil {
			merged[id] = def.Default
		}
	}
	return merged
}

func deepCopyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}
