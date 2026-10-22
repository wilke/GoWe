package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/model"
)

// ErrInputValidation is the sentinel error wrapped by every hard failure
// produced from a value/type mismatch found by this package. Callers that
// turn a validation failure into a terminal (non-retryable) error should
// wrap this sentinel, e.g. fmt.Errorf("%w: %s", ErrInputValidation, msg), so
// that errors.Is(err, ErrInputValidation) identifies it downstream.
var ErrInputValidation = errors.New("input validation")

// ParamSpec describes one declared input parameter against which a supplied
// value is checked.
//
// TypeSchema is the canonical, JSON-safe type schema for the parameter in
// the format pinned by GoWe issue #273 ("Pinned schema format" in the
// implementation plan): a primitive or unresolved named ref is a string; a
// union is a []any of members; an array is
// map[string]any{"type":"array","items":<schema>}; an enum is
// map[string]any{"type":"enum","symbols":[]any}; a record is
// map[string]any{"type":"record","fields":[]any of
// map[string]any{"name":..,"type":..}}; a recursion guard is
// map[string]any{"type":"$ref","name":..}. TypeSchema == nil means "do not
// validate this parameter" (e.g. it could not be derived from the source
// CWL).
//
// Default/HasDefault describe whether the parameter has a declared default
// value; per the CWL spec, an explicit null (or a missing value) for a
// parameter that has a default is not an error - it just means "use the
// default" - regardless of whether the declared type itself permits null.
//
// Secret marks a parameter whose value must never be echoed into a
// FieldError message (in addition to Options.ToolLevel, which redacts
// every parameter).
type ParamSpec struct {
	TypeSchema any
	Default    any
	HasDefault bool
	Secret     bool
}

// Options controls validation limits and redaction behaviour.
type Options struct {
	// MaxErrors caps the number of FieldErrors returned. Zero means the
	// default (20).
	MaxErrors int
	// MaxDepth caps how many levels of array/record nesting are walked
	// before validation gives up with a "nesting too deep" error. Zero
	// means the default (64).
	MaxDepth int
	// ToolLevel, when true, means every FieldError message omits the
	// offending value, regardless of ParamSpec.Secret. Tool-level
	// validation runs on a worker/executor where values must never be
	// logged or returned verbatim.
	ToolLevel bool
	// CheckMissing, when true, applies the required/default/nullable rules
	// to a missing or explicit-null top-level parameter value (the
	// pre-#273 behaviour). When false (the default), a missing or null
	// top-level value is never reported by this package - production
	// callers already have their own legacy required-input checks (e.g.
	// the dry-run report, the scheduler), and duplicating them here just
	// produces two error messages for the same problem. This only affects
	// the top-level present/nil check in validateParam: a required field
	// missing from a *supplied* record is still reported unconditionally.
	CheckMissing bool
}

const (
	defaultMaxErrors = 20
	defaultMaxDepth  = 64
	maxSymbolsListed = 20
	maxValueRunes    = 64
)

func (o Options) normalized() Options {
	if o.MaxErrors <= 0 {
		o.MaxErrors = defaultMaxErrors
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = defaultMaxDepth
	}
	return o
}

// primitiveNames is the fixed set of CWL primitive/pseudo-primitive type
// names recognised by the canonical schema format. Any other string schema
// is an unresolved named type reference and always validates (see
// isUnresolvedOrRef and the contract doc in testdata/schemas.json).
var primitiveNames = map[string]bool{
	"null":      true,
	"boolean":   true,
	"int":       true,
	"long":      true,
	"float":     true,
	"double":    true,
	"string":    true,
	"File":      true,
	"Directory": true,
	"Any":       true,
	"stdin":     true,
}

// collector accumulates FieldErrors for a single ValidateValue/ValidateInputs
// call (or, for a single parameter, a single validateParam call), enforcing
// the error cap and value-redaction policy along the way.
type collector struct {
	field     string
	maxErrors int
	maxDepth  int
	redact    bool
	errs      []model.FieldError
}

func (c *collector) full() bool {
	return len(c.errs) >= c.maxErrors
}

func (c *collector) add(path, msg string) {
	if c.full() {
		return
	}
	c.errs = append(c.errs, model.FieldError{Field: c.field, Path: path, Message: msg})
}

// addTypeError records a "expected X[, got <value>]" error at path,
// respecting the collector's redaction policy.
func addTypeError(c *collector, path, expected string, value any) {
	if c.full() {
		return
	}
	msg := "expected " + expected
	if !c.redact {
		msg += ", got " + formatValue(value)
	}
	c.add(path, msg)
}

func addEnumError(c *collector, path string, symbols []any, value any) {
	if c.full() {
		return
	}
	msg := fmt.Sprintf("expected one of [%s]", symbolList(symbols))
	if !c.redact {
		msg += ", got " + formatValue(value)
	}
	c.add(path, msg)
}

// ValidateValue validates a single JSON-safe value against a single
// canonical TypeSchema (see ParamSpec.TypeSchema), returning FieldErrors for
// every mismatch found (up to Options.MaxErrors). path is the starting path
// prefix for reported errors (top-level callers normally pass ""); nested
// errors extend it with "[N]" for array indices and ".name" for record
// fields. The returned FieldErrors never have Field set - callers that know
// the input id (ValidateInputs, SubmissionInputs) attach it themselves.
func ValidateValue(value any, schema any, path string, opts Options) []model.FieldError {
	opts = opts.normalized()
	c := &collector{maxErrors: opts.MaxErrors, maxDepth: opts.MaxDepth, redact: opts.ToolLevel}
	validateValue(value, schema, path, 0, c)
	sortFieldErrors(c.errs)
	return c.errs
}

// ValidateInputs validates a set of supplied inputs against their declared
// ParamSpecs. Undeclared keys in inputs are ignored; declared parameters
// missing from inputs are checked for required-ness. Returned FieldErrors
// have Field set to the parameter id and are sorted by (Field, Path).
func ValidateInputs(params map[string]ParamSpec, inputs map[string]any, opts Options) []model.FieldError {
	opts = opts.normalized()

	ids := make([]string, 0, len(params))
	for id := range params {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var all []model.FieldError
	for _, id := range ids {
		remaining := opts.MaxErrors - len(all)
		if remaining <= 0 {
			break
		}
		spec := params[id]
		value, present := inputs[id]
		paramOpts := opts
		paramOpts.MaxErrors = remaining
		all = append(all, validateParam(id, spec, value, present, paramOpts)...)
	}
	sortFieldErrors(all)
	return all
}

// SubmissionInputs validates a submission's inputs against the declared
// input types of the workflow whose stored CWL text is rawCWL. It re-parses
// rawCWL (as the scheduler does per task), so every stored workflow is
// covered without persisting schemas. ParseGraph also wraps a bare tool as a
// workflow, so tool registrations are covered too.
//
// Ids in secretInputs are validated but their values are never echoed.
// Inputs whose TypeSchema is nil, and named types the parser could not
// resolve, are skipped. A parse error is returned as err with no field
// errors: callers must treat it as "could not validate", never as a
// rejection.
func SubmissionInputs(rawCWL []byte, secretInputs []string, inputs map[string]any, opts Options) ([]model.FieldError, error) {
	graph, err := parser.New(discardLogger).ParseGraph(rawCWL)
	if err != nil {
		return nil, fmt.Errorf("parse workflow for input validation: %w", err)
	}
	if graph == nil || graph.Workflow == nil {
		return nil, nil
	}
	secret := make(map[string]bool, len(secretInputs))
	for _, id := range secretInputs {
		secret[id] = true
	}
	params := make(map[string]ParamSpec, len(graph.Workflow.Inputs))
	for id, in := range graph.Workflow.Inputs {
		id = strings.TrimPrefix(id, "#")
		params[id] = ParamSpec{
			TypeSchema: in.TypeSchema,
			Default:    in.Default,
			HasDefault: in.Default != nil,
			Secret:     secret[id],
		}
	}
	if inputs == nil {
		inputs = map[string]any{}
	}
	return ValidateInputs(params, inputs, opts), nil
}

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// validateParam applies the null/default/required rules for a single
// top-level parameter before delegating the present, non-null,
// non-secret-placeholder value to validateValue.
func validateParam(id string, spec ParamSpec, value any, present bool, opts Options) []model.FieldError {
	if spec.TypeSchema == nil {
		return nil
	}
	redact := spec.Secret || opts.ToolLevel
	c := &collector{field: id, maxErrors: opts.MaxErrors, maxDepth: opts.MaxDepth, redact: redact}

	if !present || value == nil {
		if !opts.CheckMissing {
			return nil
		}
		if spec.HasDefault || schemaAllowsNull(spec.TypeSchema) {
			return nil
		}
		c.add("", requiredMessage(spec.TypeSchema))
		return c.errs
	}

	if isSecretPlaceholder(value) {
		return nil
	}

	validateValue(value, spec.TypeSchema, "", 0, c)
	return c.errs
}

func isSecretPlaceholder(value any) bool {
	s, ok := value.(string)
	return ok && s == model.SecretInputPlaceholder
}

func requiredMessage(schema any) string {
	return fmt.Sprintf("required input missing (expected %s)", describeSchema(schema))
}

// schemaAllowsNull reports whether schema itself permits an explicit null
// (i.e. the bare "null" type, or a union that includes "null"). It is used
// both for top-level required-ness (without a default) and for record
// fields (a missing/null field is only an error when its own schema does
// not allow null).
func schemaAllowsNull(schema any) bool {
	switch s := schema.(type) {
	case string:
		return s == "null"
	case []any:
		for _, m := range s {
			if str, ok := m.(string); ok && str == "null" {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// validateValue is the recursive core. depth counts structural nesting
// (array items, record fields) and is compared against c.maxDepth.
func validateValue(value any, schema any, path string, depth int, c *collector) {
	if c.full() {
		return
	}
	if depth > c.maxDepth {
		c.add(path, "nesting too deep")
		return
	}
	switch s := schema.(type) {
	case nil:
		return
	case string:
		validatePrimitive(value, s, path, c)
	case []any:
		validateUnion(value, s, path, depth, c)
	case map[string]any:
		validateComposite(value, s, path, depth, c)
	default:
		// Unknown schema shape: never reject what we cannot understand.
	}
}

func validatePrimitive(value any, name string, path string, c *collector) {
	switch name {
	case "null":
		if value != nil {
			addTypeError(c, path, "null", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			addTypeError(c, path, "boolean", value)
		}
	case "string":
		if _, ok := value.(string); !ok {
			addTypeError(c, path, "string", value)
		}
	case "int":
		num, integral, i64, overflow, _ := classifyNumber(value)
		if !num || !integral {
			addTypeError(c, path, "int", value)
			return
		}
		if overflow || i64 < math.MinInt32 || i64 > math.MaxInt32 {
			addTypeError(c, path, "int (32-bit signed)", value)
		}
	case "long":
		num, integral, _, _, _ := classifyNumber(value)
		if !num || !integral {
			addTypeError(c, path, "long", value)
		}
	case "float", "double":
		num, _, _, _, _ := classifyNumber(value)
		if !num {
			addTypeError(c, path, name, value)
		}
	case "File", "Directory":
		validateFileOrDir(value, name, path, c)
	case "stdin":
		validateFileOrDir(value, "File", path, c)
	case "Any":
		if value == nil {
			addTypeError(c, path, "non-null value", value)
		}
	default:
		// Not one of the fixed primitive names: an unresolved named type
		// reference. Never reject what we cannot understand.
	}
}

// validateFileOrDir applies the engine's own leniency for File/Directory
// (and stdin, which behaves as File) values: the scheduler
// (normalizeDirectory in internal/scheduler/resolve.go) and the BV-BRC
// executor (internal/executor/bvbrc.go) both accept a bare non-empty string
// path and a class-less map carrying a location/path (or, for File,
// contents), normalizing either into a proper {class, location} object
// before execution. The validator must never reject what the engine
// accepts, so it mirrors that leniency here:
//
//   - a map whose class equals class: accepted (checked further only by
//     shape, not by field presence - the engine fills those in);
//   - a map without class that has a non-empty location, path, or (for
//     File) contents: accepted, since the engine normalizes it;
//   - a non-empty string: accepted, since the engine normalizes it;
//   - anything else (wrong class, a map with none of those keys, an empty
//     string, or a non-string/non-map value) is rejected.
func validateFileOrDir(value any, class string, path string, c *collector) {
	switch v := value.(type) {
	case string:
		if v == "" {
			addTypeError(c, path, fmt.Sprintf("%s (object with class %q, or a non-empty path string)", class, class), value)
		}
		return
	case map[string]any:
		if cls, hasClass := v["class"]; hasClass {
			clsStr, _ := cls.(string)
			if clsStr != class {
				addTypeError(c, path, fmt.Sprintf("%s (class %q)", class, class), value)
			}
			return
		}
		if nonEmptyStringField(v, "location") || nonEmptyStringField(v, "path") {
			return
		}
		if class == "File" && nonEmptyStringField(v, "contents") {
			return
		}
		addTypeError(c, path, fmt.Sprintf("%s (object with class %q, location, or path)", class, class), value)
	default:
		addTypeError(c, path, fmt.Sprintf("%s (object with class %q, or a non-empty path string)", class, class), value)
	}
}

// nonEmptyStringField reports whether m[key] is a string with non-zero
// length.
func nonEmptyStringField(m map[string]any, key string) bool {
	s, ok := m[key].(string)
	return ok && s != ""
}

func validateComposite(value any, m map[string]any, path string, depth int, c *collector) {
	t, _ := m["type"].(string)
	switch t {
	case "array":
		validateArray(value, m["items"], path, depth, c)
	case "record":
		fields, _ := m["fields"].([]any)
		validateRecord(value, fields, path, depth, c)
	case "enum":
		symbols, _ := m["symbols"].([]any)
		validateEnum(value, symbols, path, c)
	case "$ref":
		// Recursion guard: unresolved by construction, always valid.
	default:
		// Unknown map shape: never reject what we cannot understand.
	}
}

func validateArray(value any, itemSchema any, path string, depth int, c *collector) {
	arr, ok := value.([]any)
	if !ok {
		addTypeError(c, path, "array", value)
		return
	}
	for i, item := range arr {
		if c.full() {
			return
		}
		validateValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i), depth+1, c)
	}
}

func validateRecord(value any, fields []any, path string, depth int, c *collector) {
	m, ok := value.(map[string]any)
	if !ok {
		addTypeError(c, path, "record (object)", value)
		return
	}
	for _, f := range fields {
		if c.full() {
			return
		}
		fm, _ := f.(map[string]any)
		rawName, _ := fm["name"].(string)
		fieldSchema := fm["type"]
		short := shortName(rawName)
		fieldPath := path + "." + short

		fv, present := lookupByShortName(m, short)
		if !present || fv == nil {
			if schemaAllowsNull(fieldSchema) {
				continue
			}
			c.add(fieldPath, fmt.Sprintf("missing required field %q", short))
			continue
		}
		validateValue(fv, fieldSchema, fieldPath, depth+1, c)
	}
}

func lookupByShortName(m map[string]any, short string) (any, bool) {
	if v, ok := m[short]; ok {
		return v, true
	}
	for k, v := range m {
		if shortName(k) == short {
			return v, true
		}
	}
	return nil, false
}

func validateEnum(value any, symbols []any, path string, c *collector) {
	str, ok := value.(string)
	if !ok {
		addEnumError(c, path, symbols, value)
		return
	}
	short := shortName(str)
	for _, sym := range symbols {
		symStr, _ := sym.(string)
		if shortName(symStr) == short {
			return
		}
	}
	addEnumError(c, path, symbols, value)
}

func validateUnion(value any, members []any, path string, depth int, c *collector) {
	// Contract rule: a union containing an unresolvable/$ref member is
	// always valid - never reject what we cannot fully understand.
	for _, m := range members {
		if isUnresolvedOrRef(m) {
			return
		}
	}
	// Optional type (["null", T]): a non-null value can only match T, so
	// validate against T directly to keep its detailed error (e.g. the
	// allowed enum symbols for an optional enum) instead of a generic
	// "one of [null, enum]".
	if value != nil {
		var nonNull []any
		for _, m := range members {
			if str, ok := m.(string); ok && str == "null" {
				continue
			}
			nonNull = append(nonNull, m)
		}
		if len(nonNull) == 1 {
			validateValue(value, nonNull[0], path, depth, c)
			return
		}
	}
	for _, m := range members {
		if schemaValidatesAt(value, m, depth, c.maxDepth) {
			return
		}
	}
	names := make([]string, 0, len(members))
	for _, m := range members {
		names = append(names, describeSchema(m))
	}
	addTypeError(c, path, fmt.Sprintf("one of [%s]", strings.Join(names, ", ")), value)
}

// schemaValidatesAt tries a single union member in isolation, stopping at
// its first error (per the contract: "stop evaluating a member at its
// first error"), and reports whether it validated cleanly. It never mutates
// the caller's collector.
func schemaValidatesAt(value any, schema any, depth int, maxDepth int) bool {
	c := &collector{maxErrors: 1, maxDepth: maxDepth}
	validateValue(value, schema, "", depth, c)
	return len(c.errs) == 0
}

func isUnresolvedOrRef(schema any) bool {
	switch v := schema.(type) {
	case string:
		return !primitiveNames[v]
	case map[string]any:
		t, _ := v["type"].(string)
		return t == "$ref"
	default:
		return false
	}
}

// describeSchema renders a short, human-readable name for a schema, used in
// "expected <...>" messages (required-input and union-mismatch errors).
func describeSchema(schema any) string {
	switch s := schema.(type) {
	case nil:
		return "any"
	case string:
		return s
	case []any:
		names := make([]string, 0, len(s))
		for _, m := range s {
			names = append(names, describeSchema(m))
		}
		return strings.Join(names, "|")
	case map[string]any:
		t, _ := s["type"].(string)
		switch t {
		case "array":
			return describeSchema(s["items"]) + "[]"
		case "record":
			return "record"
		case "enum":
			symbols, _ := s["symbols"].([]any)
			return "enum{" + symbolList(symbols) + "}"
		case "$ref":
			return "ref"
		default:
			return "object"
		}
	default:
		return "value"
	}
}

// shortName returns the segment of s after the last '/' or '#', or s
// unchanged if it contains neither. Used to match packed/named-type ids
// (e.g. "#capture_kit.yml/capture_kit/bait") against their local names on
// both sides of a comparison (enum symbols, record field names).
func shortName(s string) string {
	if i := strings.LastIndexAny(s, "/#"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// symbolList renders enum symbols (shortened) for an error message,
// capping the listed count at maxSymbolsListed and summarising the rest as
// "...(N more)".
func symbolList(symbols []any) string {
	names := make([]string, 0, len(symbols))
	for _, s := range symbols {
		if str, ok := s.(string); ok {
			names = append(names, shortName(str))
		}
	}
	if len(names) > maxSymbolsListed {
		more := len(names) - maxSymbolsListed
		return strings.Join(names[:maxSymbolsListed], ", ") + fmt.Sprintf(", …(%d more)", more)
	}
	return strings.Join(names, ", ")
}

// formatValue renders value as a %q-quoted, length-capped string for
// inclusion in an error message. Non-string values are rendered via JSON
// first so every message ends up as a single quoted, escaped token (this is
// also what protects messages from embedded control characters such as
// newlines in the offending value).
func formatValue(value any) string {
	var s string
	switch v := value.(type) {
	case string:
		s = v
	case json.Number:
		s = v.String()
	default:
		if b, err := json.Marshal(v); err == nil {
			s = string(b)
		} else {
			s = fmt.Sprintf("%v", v)
		}
	}
	if r := []rune(s); len(r) > maxValueRunes {
		s = string(r[:maxValueRunes])
	}
	return fmt.Sprintf("%q", s)
}

// classifyNumber inspects value across every Go numeric kind plus
// json.Number and reports: whether it is numeric at all, whether it holds
// an integral value, its int64 value (valid only when integral && !overflow),
// whether that int64 conversion overflows, and its float64 value.
func classifyNumber(value any) (isNumber bool, integral bool, i64 int64, overflow bool, f64 float64) {
	switch v := value.(type) {
	case int:
		return true, true, int64(v), false, float64(v)
	case int8:
		return true, true, int64(v), false, float64(v)
	case int16:
		return true, true, int64(v), false, float64(v)
	case int32:
		return true, true, int64(v), false, float64(v)
	case int64:
		return true, true, v, false, float64(v)
	case uint:
		if uint64(v) > math.MaxInt64 {
			return true, true, 0, true, float64(v)
		}
		return true, true, int64(v), false, float64(v)
	case uint8:
		return true, true, int64(v), false, float64(v)
	case uint16:
		return true, true, int64(v), false, float64(v)
	case uint32:
		return true, true, int64(v), false, float64(v)
	case uint64:
		if v > math.MaxInt64 {
			return true, true, 0, true, float64(v)
		}
		return true, true, int64(v), false, float64(v)
	case float32:
		f := float64(v)
		return true, f == math.Trunc(f), int64(f), false, f
	case float64:
		integral := v == math.Trunc(v)
		if integral && (v > math.MaxInt64 || v < math.MinInt64) {
			return true, true, 0, true, v
		}
		return true, integral, int64(v), false, v
	case json.Number:
		if iv, err := v.Int64(); err == nil {
			return true, true, iv, false, float64(iv)
		}
		if fv, err := strconv.ParseFloat(v.String(), 64); err == nil {
			integral := fv == math.Trunc(fv)
			if integral && (fv > math.MaxInt64 || fv < math.MinInt64) {
				return true, true, 0, true, fv
			}
			return true, integral, int64(fv), false, fv
		}
		return false, false, 0, false, 0
	default:
		return false, false, 0, false, 0
	}
}

func sortFieldErrors(errs []model.FieldError) {
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].Field != errs[j].Field {
			return errs[i].Field < errs[j].Field
		}
		return errs[i].Path < errs[j].Path
	})
}
