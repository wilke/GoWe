package parser

import (
	"sort"
	"strings"
)

// primitiveTypeNames are the CWL primitive/pseudo type names that are never
// resolved as SchemaDefRequirement named types.
var primitiveTypeNames = map[string]bool{
	"null": true, "boolean": true, "int": true, "long": true,
	"float": true, "double": true, "string": true,
	"File": true, "Directory": true, "Any": true, "stdin": true,
}

// schemaDefs is a lookup table of named CWL types collected from
// SchemaDefRequirement.types (see collectSchemaDefsFromRequirements). Lookup
// is exact-name-first, then short-name (the segment after the last '/' or
// '#') only when the short name is unambiguous across all registered defs.
//
// This is the contract described in plan-273.md section "Key design
// decisions" #3 and the "Pinned schema format" section: named types are
// inlined at parse time; a name that isn't found (or is ambiguous) is left
// as an unresolved string, which the validator treats as always-valid.
type schemaDefs struct {
	byName  map[string]map[string]any // exact declared name -> raw type def map
	byShort map[string][]string       // short name -> declared names sharing it
}

func newSchemaDefs() *schemaDefs {
	return &schemaDefs{
		byName:  make(map[string]map[string]any),
		byShort: make(map[string][]string),
	}
}

// shortName returns the segment of name after the last '/' or '#', or name
// itself if neither separator is present.
func shortName(name string) string {
	if i := strings.LastIndexAny(name, "/#"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// add registers a single named type definition (a raw map with a "name"
// key, as found in SchemaDefRequirement.types). Entries without a usable
// name are ignored. The first registration for a given exact name wins.
func (d *schemaDefs) add(def map[string]any) {
	if d == nil || def == nil {
		return
	}
	name, _ := def["name"].(string)
	if name == "" {
		return
	}
	if _, exists := d.byName[name]; exists {
		return
	}
	d.byName[name] = def
	short := shortName(name)
	d.byShort[short] = append(d.byShort[short], name)
}

// merge returns a schemaDefs containing the entries of d and other. Entries
// already present in d are not overwritten by other (see add).
func (d *schemaDefs) merge(other *schemaDefs) *schemaDefs {
	if other == nil || len(other.byName) == 0 {
		if d == nil {
			return newSchemaDefs()
		}
		return d
	}
	if d == nil || len(d.byName) == 0 {
		return other
	}
	merged := newSchemaDefs()
	for _, def := range d.byName {
		merged.add(def)
	}
	for _, def := range other.byName {
		merged.add(def)
	}
	return merged
}

// lookupWithName resolves a type reference string to its raw definition and
// the exact declared name it resolved to. Exact match first; falling back
// to short-name match only when the short name is unambiguous.
func (d *schemaDefs) lookupWithName(ref string) (def map[string]any, resolvedName string, ok bool) {
	if d == nil {
		return nil, "", false
	}
	if def, ok := d.byName[ref]; ok {
		return def, ref, true
	}
	short := shortName(ref)
	if names, ok := d.byShort[short]; ok && len(names) == 1 {
		return d.byName[names[0]], names[0], true
	}
	return nil, "", false
}

// collectSchemaDefsFromRequirements extracts SchemaDefRequirement.types from
// a requirements map already normalized to map-form (keyed by class), as
// produced by normalizeHintsToMap. normalizeHintsToMap folds both CWL
// requirement forms (list-form `requirements: [{class: ..., types: [...]}]`
// and map-form `requirements: {SchemaDefRequirement: {types: [...]}}`) into
// this same shape, so a single extraction covers both.
func collectSchemaDefsFromRequirements(reqs map[string]any) *schemaDefs {
	d := newSchemaDefs()
	if reqs == nil {
		return d
	}
	sdrRaw, ok := reqs["SchemaDefRequirement"]
	if !ok {
		return d
	}
	sdr, ok := sdrRaw.(map[string]any)
	if !ok {
		return d
	}
	types, ok := sdr["types"].([]any)
	if !ok {
		return d
	}
	for _, t := range types {
		if tm, ok := t.(map[string]any); ok {
			d.add(tm)
		}
	}
	return d
}

// defsFromRawRequirements normalizes raw["requirements"] (either CWL form)
// and collects its SchemaDefRequirement types, merged with parent.
func defsFromRawRequirements(raw map[string]any, parent *schemaDefs) *schemaDefs {
	local := collectSchemaDefsFromRequirements(normalizeHintsToMap(raw["requirements"]))
	return parent.merge(local)
}

// normalizeType converts a raw CWL type expression (as decoded from
// YAML/JSON: string, []any, or map[string]any) into the pinned canonical
// TypeSchema form documented in plan-273.md and
// internal/validate/testdata/schemas.json:
//
//   - primitive or unresolved named ref: string
//   - union: []any of normalized members (optional "T?" => ["null", T])
//   - array: map[string]any{"type": "array", "items": <schema>}
//   - enum: map[string]any{"type": "enum", "symbols": [...]} (symbols kept raw)
//   - record: map[string]any{"type": "record", "fields": [...]}
//   - recursion guard: map[string]any{"type": "$ref", "name": <name>}
//
// seen tracks named types currently being inlined along the current
// recursion path (to break cycles); pass a fresh map for each independent
// top-level type being normalized.
func normalizeType(raw any, defs *schemaDefs, seen map[string]bool) any {
	if seen == nil {
		seen = make(map[string]bool)
	}
	switch t := raw.(type) {
	case nil:
		return nil
	case string:
		return normalizeTypeString(t, defs, seen)
	case []any:
		members := make([]any, len(t))
		for i, m := range t {
			members[i] = normalizeType(m, defs, seen)
		}
		return members
	case map[string]any:
		return normalizeTypeMap(t, defs, seen)
	default:
		return raw
	}
}

// normalizeTypeString expands shorthand suffixes ("T?", "T[]", "T[]?"),
// passes through primitives, and inlines named SchemaDefRequirement types
// (exact name, then unique short name). An unknown name stays a string.
func normalizeTypeString(s string, defs *schemaDefs, seen map[string]bool) any {
	switch {
	case strings.HasSuffix(s, "[]?"):
		base := strings.TrimSuffix(s, "[]?")
		return []any{"null", map[string]any{
			"type":  "array",
			"items": normalizeTypeString(base, defs, seen),
		}}
	case strings.HasSuffix(s, "[]"):
		base := strings.TrimSuffix(s, "[]")
		return map[string]any{
			"type":  "array",
			"items": normalizeTypeString(base, defs, seen),
		}
	case strings.HasSuffix(s, "?"):
		base := strings.TrimSuffix(s, "?")
		return []any{"null", normalizeTypeString(base, defs, seen)}
	}

	if primitiveTypeNames[s] {
		return s
	}

	def, resolvedName, ok := defs.lookupWithName(s)
	if !ok {
		// Unresolved named ref: stays a string (contract rule).
		return s
	}
	if seen[resolvedName] {
		return map[string]any{"type": "$ref", "name": resolvedName}
	}
	nextSeen := make(map[string]bool, len(seen)+1)
	for k := range seen {
		nextSeen[k] = true
	}
	nextSeen[resolvedName] = true
	return normalizeTypeMap(def, defs, nextSeen)
}

// normalizeTypeMap normalizes a map-form type expression: array, enum,
// record, or (as a schemaDefs entry body) any of those tagged with a "name"
// that normalizeTypeMap ignores (name/label/doc/inputBinding are dropped).
func normalizeTypeMap(m map[string]any, defs *schemaDefs, seen map[string]bool) any {
	base, _ := m["type"].(string)
	switch base {
	case "array":
		return map[string]any{
			"type":  "array",
			"items": normalizeType(m["items"], defs, seen),
		}
	case "enum":
		return map[string]any{
			"type":    "enum",
			"symbols": rawSymbols(m["symbols"]),
		}
	case "record":
		return map[string]any{
			"type":   "record",
			"fields": normalizeRecordFieldsSchema(m["fields"], defs, seen),
		}
	default:
		// The "type" key isn't one of array/enum/record (e.g. absent, or a
		// nested type expression) — normalize it recursively rather than
		// silently dropping information.
		if t, ok := m["type"]; ok && t != nil {
			return normalizeType(t, defs, seen)
		}
		return m
	}
}

// rawSymbols copies an enum's symbols list as-is (contract: "symbols stored
// raw; validator shortens").
func rawSymbols(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return []any{}
	}
	out := make([]any, len(arr))
	copy(out, arr)
	return out
}

// normalizeRecordFieldsSchema normalizes a record type's "fields" into the
// canonical list form: [{"name": <raw>, "type": <schema>}, ...]. List-form
// fields keep their declared order; map-form fields (fieldName -> type or
// field-definition-map) are sorted by key.
func normalizeRecordFieldsSchema(fields any, defs *schemaDefs, seen map[string]bool) []any {
	switch f := fields.(type) {
	case []any:
		out := make([]any, 0, len(f))
		for _, item := range f {
			fm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := fm["name"].(string)
			out = append(out, map[string]any{
				"name": name,
				"type": normalizeType(fm["type"], defs, seen),
			})
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(f))
		for k := range f {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, map[string]any{
				"name": k,
				"type": normalizeType(recordFieldTypeValue(f[k]), defs, seen),
			})
		}
		return out
	}
	return nil
}

// recordFieldTypeValue extracts the type expression from a map-form record
// field's value. A bare string or list IS the type directly (CWL shorthand,
// mirrors the workflow/tool input map-form). A map is a field-definition
// wrapper ({type, doc, inputBinding, ...}); its "type" key is the type.
func recordFieldTypeValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return val["type"]
	default:
		return v
	}
}
