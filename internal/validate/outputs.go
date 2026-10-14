package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// ErrOutputValidation is the sentinel wrapped by every failure produced by
// ToolOutputs (a collected tool output that is missing, null, or does not
// match its declared type; #279/#310). errors.Is(err, ErrOutputValidation)
// identifies it downstream.
var ErrOutputValidation = errors.New("output validation")

// ToolOutputs validates the outputs collected from a CommandLineTool run
// against the tool's declared output types, reusing the #273 type checker
// (ValidateInputs / validateValue) so inputs and outputs follow one set of
// type rules.
//
//   - A declared output that is missing or null while its type does not
//     allow null fails (a File glob that matched nothing, a required key
//     absent from cwl.output.json).
//   - A value that does not match the declared type (enum symbol, primitive,
//     File/Directory class, array items, record fields, unions) fails.
//   - Keys that are not declared outputs are removed from the returned map,
//     with one WARN log per key.
//
// Outputs whose TypeSchema is nil are not validated. Messages never include
// the offending value (Options.ToolLevel). The returned map is outputs with
// undeclared keys dropped (outputs itself is not modified); on failure the
// error wraps ErrOutputValidation and names every offending output id (up
// to the error cap).
func ToolOutputs(tool *cwl.CommandLineTool, outputs map[string]any, logger *slog.Logger) (map[string]any, error) {
	if tool == nil {
		return outputs, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	declared := make(map[string]string, len(tool.Outputs)) // trimmed id -> declared key
	params := make(map[string]ParamSpec, len(tool.Outputs))
	for id, out := range tool.Outputs {
		short := trimOutputID(id)
		declared[short] = id
		params[short] = ParamSpec{TypeSchema: outputSchema(out.TypeSchema)}
	}

	cleaned := make(map[string]any, len(outputs))
	var extras []string
	for k, v := range outputs {
		if _, ok := declared[trimOutputID(k)]; ok {
			cleaned[trimOutputID(k)] = v
		} else {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		logger.Warn("dropping undeclared tool output", "tool", tool.ID, "output", k)
	}

	// Collected outputs may hold Go-typed values ([]map[string]any from a
	// glob, []string, int64, ...) that the JSON-shaped type checker does not
	// recognise, so validate a JSON round-tripped view. The caller still
	// receives the original values.
	view := make(map[string]any, len(cleaned))
	for k, v := range cleaned {
		view[k] = jsonView(v)
	}

	errs := ValidateInputs(params, view, Options{ToolLevel: true, CheckMissing: true})
	for i := range errs {
		errs[i].Message = strings.Replace(errs[i].Message, "required input missing", "required output missing", 1)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrOutputValidation, summarizeOutputErrors(errs))
	}

	// Return values under the keys the caller declared (not the trimmed form).
	result := make(map[string]any, len(cleaned))
	for short, v := range cleaned {
		result[declared[short]] = v
	}
	return result, nil
}

// jsonView returns v as encoding/json would decode it (map[string]any,
// []any, float64, string, bool, nil). A value that cannot be marshalled is
// returned unchanged.
func jsonView(v any) any {
	switch v.(type) {
	case nil, string, bool, float64:
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

// trimOutputID strips a leading "#" (and any "<tool>/" prefix) from an
// output id so collected keys and declared ids compare equal.
func trimOutputID(id string) string {
	id = strings.TrimPrefix(id, "#")
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// outputSchema maps the output-only pseudo types stdout/stderr (a File
// captured from the process streams) onto File; every other schema is used
// as-is. Unions are mapped member by member.
func outputSchema(schema any) any {
	switch s := schema.(type) {
	case string:
		if s == "stdout" || s == "stderr" {
			return "File"
		}
		return s
	case []any:
		out := make([]any, len(s))
		for i, m := range s {
			out[i] = outputSchema(m)
		}
		return out
	default:
		return schema
	}
}

// summarizeOutputErrors renders a one-line summary naming the first
// offending output, plus a count of the rest.
func summarizeOutputErrors(errs []model.FieldError) string {
	first := errs[0]
	msg := fmt.Sprintf("output %q%s: %s", first.Field, first.Path, first.Message)
	if len(errs) > 1 {
		ids := make([]string, 0, len(errs)-1)
		seen := map[string]bool{first.Field: true}
		for _, e := range errs[1:] {
			if !seen[e.Field] {
				seen[e.Field] = true
				ids = append(ids, e.Field)
			}
		}
		msg += fmt.Sprintf(" (+%d more errors", len(errs)-1)
		if len(ids) > 0 {
			msg += "; also outputs: " + strings.Join(ids, ", ")
		}
		msg += ")"
	}
	return msg
}

// IsPermanent reports whether err is an input- or output-validation failure:
// one that would fail identically on every retry, so callers should make it
// terminal rather than consume the retry budget.
func IsPermanent(err error) bool {
	return errors.Is(err, ErrInputValidation) || errors.Is(err, ErrOutputValidation)
}
