// Package toolhash computes the content digest an administrator blesses when
// allowing a tool to receive a user's token, and the human review summary that
// accompanies it.
//
// The digest is taken over the canonical JSON of the FINAL post-merge tool
// document (requirements and hints, after workflow/step/job requirements have
// been merged in by internal/toolmerge), so that bless time, submit time and
// dispatch time agree byte for byte. Classes that cannot change what code runs
// are stripped symmetrically (see benignClasses); everything else is hashed
// (default-deny).
package toolhash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/toolmerge"
	"github.com/me/gowe/pkg/cwl"
)

// Version is the canonicalisation version. It is the first line of the hashed
// preimage ("v1\n"), so a future change of the canonical form cannot collide
// with a v1 digest.
const Version = 1

// benignClasses are requirement/hint classes whose presence or content cannot
// change which code runs or which image is launched. They are removed from
// both "requirements" and "hints" before hashing. InlineJavascriptRequirement
// is benign only without an expressionLib (handled separately).
var benignClasses = map[string]bool{
	"ScatterFeatureRequirement":              true,
	"StepInputExpressionRequirement":         true,
	"SubworkflowFeatureRequirement":          true,
	"MultipleInputFeatureRequirement":        true,
	"ResourceRequirement":                    true,
	"SchemaDefRequirement":                   true,
	"LoadListingRequirement":                 true,
	"WorkReuse":                              true,
	"ToolTimeLimit":                          true,
	"gowe:ResourceData":                      true,
	"https://gowe.commonwl.org#ResourceData": true,
}

// strippedTopLevel are document keys that carry identity or namespace
// bookkeeping, not behaviour.
var strippedTopLevel = []string{"id", "cwlVersion", "$namespaces", "$schemas"}

func isBenign(class string, body any) bool {
	if benignClasses[class] {
		return true
	}
	if strings.HasSuffix(class, "#ResourceData") && (strings.Contains(class, "gowe") || strings.Contains(class, "GoWe")) {
		return true
	}
	if class == "InlineJavascriptRequirement" {
		m, ok := body.(map[string]any)
		if !ok {
			return true
		}
		switch lib := m["expressionLib"].(type) {
		case nil:
			return true
		case []any:
			return len(lib) == 0
		case string:
			return lib == ""
		default:
			return false
		}
	}
	return false
}

// normalize deep-copies v through JSON so numbers and nested types are
// canonical and the caller's map is never mutated.
func normalize(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal tool: %w", err)
	}
	return out, nil
}

// stripSection removes benign classes from a requirements/hints value, which
// may be a class-keyed map or a list of {class: ...} objects. It returns nil
// when nothing is left, so absent and empty sections hash identically.
func stripSection(v any) any {
	switch sec := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(sec))
		for class, body := range sec {
			if isBenign(class, body) {
				continue
			}
			out[class] = withoutClass(class, body)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		// List form {class: X, ...} is canonicalised to the map form
		// {X: {...}} so both registrations of a tool hash alike.
		asMap := map[string]any{}
		for _, item := range sec {
			m, ok := item.(map[string]any)
			class, _ := m["class"].(string)
			if !ok || class == "" {
				return stripUnclassedList(sec)
			}
			if isBenign(class, m) {
				continue
			}
			asMap[class] = withoutClass(class, m)
		}
		if len(asMap) == 0 {
			return nil
		}
		return asMap
	default:
		return v
	}
}

// withoutClass drops a redundant "class" field equal to the section key.
func withoutClass(class string, body any) any {
	m, ok := body.(map[string]any)
	if !ok || m["class"] != class {
		return body
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		if k != "class" {
			cp[k] = v
		}
	}
	return cp
}

// stripUnclassedList is the fallback for a malformed list (items without a
// class): benign classed items are still dropped, the rest is kept verbatim.
func stripUnclassedList(sec []any) any {
	out := make([]any, 0, len(sec))
	for _, item := range sec {
		if m, ok := item.(map[string]any); ok {
			if c, _ := m["class"].(string); c != "" && isBenign(c, m) {
				continue
			}
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Canonical returns the deterministic JSON a tool is hashed over.
func Canonical(tool map[string]any) ([]byte, error) {
	doc, err := normalize(tool)
	if err != nil {
		return nil, err
	}
	for _, k := range strippedTopLevel {
		delete(doc, k)
	}
	for _, sect := range []string{"requirements", "hints"} {
		if v, ok := doc[sect]; ok {
			if s := stripSection(v); s == nil {
				delete(doc, sect)
			} else {
				doc[sect] = s
			}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil { // map keys are sorted by encoding/json
		return nil, fmt.Errorf("encode canonical tool: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Digest returns "sha256:" + hex(sha256("v1\n" + canonical JSON)).
func Digest(tool map[string]any) (string, error) {
	canon, err := Canonical(tool)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "v%d\n", Version)
	h.Write(canon)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// ImageRef returns the image reference the worker launches for the tool:
// DockerRequirement.dockerPull from requirements, else from hints. It mirrors
// cwltool.GetDockerImage (a test asserts parity) without depending on it.
func ImageRef(tool map[string]any) string {
	for _, sect := range []string{"requirements", "hints"} {
		switch s := tool[sect].(type) {
		case map[string]any:
			if dr, ok := s["DockerRequirement"].(map[string]any); ok {
				if pull, ok := dr["dockerPull"].(string); ok {
					return pull
				}
			}
		case []any:
			for _, item := range s {
				if dr, ok := item.(map[string]any); ok && dr["class"] == "DockerRequirement" {
					if pull, ok := dr["dockerPull"].(string); ok {
						return pull
					}
				}
			}
		}
	}
	return ""
}

// StepResult is the outcome of DigestForStep.
type StepResult struct {
	// Hash is the digest of the post-merge tool.
	Hash string
	// PristineHash is the digest of the registered tool before any merge
	// (what a blessing of the standalone tool records).
	PristineHash string
	// ToolID is the graph id of the tool the step runs.
	ToolID string
	// ImageRef is the image the worker would launch.
	ImageRef string
	// Inherited lists the non-benign requirement/hint classes the merged tool
	// has that the registered tool does not (sorted, unique).
	Inherited []string
	// Tool is the merged tool map (callers may Summarize it).
	Tool map[string]any
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// graphForPath resolves a step path ("step" or "outer/inner") to the graph
// document and step id the scheduler would merge against. Each non-final
// segment descends into the sub-workflow child root exactly as the scheduler
// builds it (toolmerge.ChildGraphForStep), re-parsing from the child's raw
// CWL, never the parent's.
func graphForPath(rawCWL, stepPath string) (*cwl.GraphDocument, string, error) {
	segs := strings.Split(strings.Trim(stepPath, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return nil, "", fmt.Errorf("empty step path")
	}
	raw := rawCWL
	for _, seg := range segs[:len(segs)-1] {
		child, err := toolmerge.ChildGraphForStep(raw, seg)
		if err != nil {
			return nil, "", fmt.Errorf("descend into sub-workflow step %q: %w", seg, err)
		}
		raw = child
	}
	graph, err := parser.New(discardLogger()).ParseGraph([]byte(raw))
	if err != nil {
		return nil, "", fmt.Errorf("parse CWL: %w", err)
	}
	return graph, segs[len(segs)-1], nil
}

func lookupTool(graph *cwl.GraphDocument, toolID string) (*cwl.CommandLineTool, bool) {
	for id, clt := range graph.Tools {
		if strings.TrimPrefix(id, "#") == toolID || id == toolID {
			return clt, true
		}
	}
	return nil, false
}

// DigestForStep computes the digest of the tool a workflow step would run:
// the registered tool merged with the step's, the (child-root) workflow's and
// the job's cwl:requirements — the same inputs and code populateToolAndJob
// uses. stepPath addresses nested steps with "/" ("cga/annotate").
func DigestForStep(rawCWL, stepPath string, jobReqs []any) (*StepResult, error) {
	graph, stepID, err := graphForPath(rawCWL, stepPath)
	if err != nil {
		return nil, err
	}
	if graph.Workflow == nil {
		return nil, fmt.Errorf("document has no workflow")
	}
	step, ok := graph.Workflow.Steps[stepID]
	if !ok {
		return nil, fmt.Errorf("step %q not found", stepID)
	}
	toolID := strings.TrimPrefix(step.Run, "#")
	clt, ok := lookupTool(graph, toolID)
	if !ok {
		return nil, fmt.Errorf("step %q does not run a CommandLineTool (run: %q)", stepID, step.Run)
	}

	pristine, err := toolmerge.StructToToolMap(clt)
	if err != nil {
		return nil, err
	}
	merged, err := toolmerge.StructToToolMap(clt)
	if err != nil {
		return nil, err
	}
	toolmerge.MergeRequirementsIntoTool(merged, graph.Workflow, stepID)
	if len(jobReqs) > 0 {
		toolmerge.MergeJobRequirementsIntoTool(merged, jobReqs)
	}

	hash, err := Digest(merged)
	if err != nil {
		return nil, err
	}
	pristineHash, err := Digest(pristine)
	if err != nil {
		return nil, err
	}
	return &StepResult{
		Hash:         hash,
		PristineHash: pristineHash,
		ToolID:       toolID,
		ImageRef:     ImageRef(merged),
		Inherited:    inheritedClasses(pristine, merged),
		Tool:         merged,
	}, nil
}

// classSet returns "section/class" keys of the non-benign classes in tool.
func classSet(tool map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, sect := range []string{"requirements", "hints"} {
		switch s := tool[sect].(type) {
		case map[string]any:
			for class, body := range s {
				if !isBenign(class, body) {
					out[sect+"/"+class] = true
				}
			}
		case []any:
			for _, item := range s {
				if m, ok := item.(map[string]any); ok {
					class, _ := m["class"].(string)
					if class != "" && !isBenign(class, m) {
						out[sect+"/"+class] = true
					}
				}
			}
		}
	}
	return out
}

func inheritedClasses(pristine, merged map[string]any) []string {
	before := classSet(pristine)
	seen := map[string]bool{}
	var out []string
	for key := range classSet(merged) {
		if before[key] {
			continue
		}
		class := key[strings.Index(key, "/")+1:]
		if !seen[class] {
			seen[class] = true
			out = append(out, class)
		}
	}
	sort.Strings(out)
	return out
}

// StandaloneResult is the outcome of DigestStandalone.
type StandaloneResult struct {
	Hash     string
	ToolID   string
	ImageRef string
	Tool     map[string]any
}

// DigestStandalone digests a registered tool on its own (no merges). rawCWL is
// either a bare CommandLineTool document or a packed $graph; toolID selects
// the tool (empty = the only tool in the document). It always goes through
// the parser and toolmerge.StructToToolMap so the pre-merge map is byte
// identical to the one the scheduler starts from.
func DigestStandalone(rawCWL, toolID string) (*StandaloneResult, error) {
	graph, err := parser.New(discardLogger()).ParseGraph([]byte(rawCWL))
	if err != nil {
		return nil, fmt.Errorf("parse CWL: %w", err)
	}
	return DigestStandaloneGraph(graph, toolID)
}

// DigestStandaloneGraph is DigestStandalone for an already parsed document.
func DigestStandaloneGraph(graph *cwl.GraphDocument, toolID string) (*StandaloneResult, error) {
	toolID = strings.TrimPrefix(toolID, "#")
	var clt *cwl.CommandLineTool
	if toolID == "" {
		if len(graph.Tools) != 1 {
			ids := make([]string, 0, len(graph.Tools))
			for id := range graph.Tools {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return nil, fmt.Errorf("document has %d tools (%s); specify the tool id", len(graph.Tools), strings.Join(ids, ", "))
		}
		for id, t := range graph.Tools {
			clt, toolID = t, strings.TrimPrefix(id, "#")
		}
	} else {
		var ok bool
		if clt, ok = lookupTool(graph, toolID); !ok {
			return nil, fmt.Errorf("tool %q not found", toolID)
		}
	}
	tool, err := toolmerge.StructToToolMap(clt)
	if err != nil {
		return nil, err
	}
	hash, err := Digest(tool)
	if err != nil {
		return nil, err
	}
	return &StandaloneResult{Hash: hash, ToolID: toolID, ImageRef: ImageRef(tool), Tool: tool}, nil
}

// ToolRequestsToken reports whether the tool's hints ask for token injection
// (gowe:Execution.inject_bvbrc_token). Informational: nothing enforces
// blessings yet.
func ToolRequestsToken(tool map[string]any) bool {
	hints, _ := tool["hints"].(map[string]any)
	for class, body := range hints {
		if IsExecutionClass(class) {
			if m, ok := body.(map[string]any); ok {
				if v, ok := m["inject_bvbrc_token"].(bool); ok && v {
					return true
				}
			}
		}
	}
	return false
}

// ToolIDs returns the sorted ids of the CommandLineTools in a CWL document.
func ToolIDs(rawCWL string) ([]string, error) {
	graph, err := parser.New(discardLogger()).ParseGraph([]byte(rawCWL))
	if err != nil {
		return nil, fmt.Errorf("parse CWL: %w", err)
	}
	ids := make([]string, 0, len(graph.Tools))
	for id := range graph.Tools {
		ids = append(ids, strings.TrimPrefix(id, "#"))
	}
	sort.Strings(ids)
	return ids, nil
}

// IsExecutionClass reports whether a hint/requirement key is the gowe:Execution
// extension in any accepted spelling (prefixed, legacy goweHint, or full URI).
func IsExecutionClass(class string) bool {
	return parser.IsExecutionClass(class)
}
