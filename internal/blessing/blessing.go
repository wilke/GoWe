// Package blessing holds the admin-side logic for tool blessings shared by the
// REST handlers (internal/server) and the admin UI (internal/ui): resolving a
// registered tool to its content digest, validating a blessing request, and
// listing registered tools that still need a blessing.
//
// Nothing here is consulted at submission or dispatch time yet.
package blessing

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

// Error is a failure with an HTTP-style status the caller can map to a
// response (400 validation, 404 not found, 409 conflict, 500 internal).
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

func errf(status int, format string, a ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, a...)}
}

// Target is a tool resolved to its standalone digest.
type Target struct {
	Result       *toolhash.StandaloneResult
	WorkflowID   string
	WorkflowName string
}

// DisplayName is "<workflow>#<tool>" (or just the tool id).
func (t *Target) DisplayName() string {
	name := t.Result.ToolID
	if t.WorkflowName != "" && t.WorkflowName != name {
		name = t.WorkflowName + "#" + name
	}
	return name
}

// ResolveWorkflow looks a workflow up by id, then by name.
func ResolveWorkflow(ctx context.Context, st store.Store, ref string) (*model.Workflow, error) {
	wf, err := st.GetWorkflow(ctx, ref)
	if err != nil {
		return nil, err
	}
	if wf != nil {
		return wf, nil
	}
	return st.GetWorkflowByName(ctx, ref)
}

// TargetFromWorkflow digests the tool toolID (empty = the only tool) of a
// registered workflow, recomputing from the stored RawCWL.
func TargetFromWorkflow(ctx context.Context, st store.Store, workflowRef, toolID string) (*Target, *Error) {
	wf, err := ResolveWorkflow(ctx, st, workflowRef)
	if err != nil {
		return nil, errf(http.StatusInternalServerError, "%v", err)
	}
	if wf == nil {
		return nil, errf(http.StatusNotFound, "workflow '%s' not found", workflowRef)
	}
	t, e := TargetFromRaw(wf.RawCWL, toolID)
	if e != nil {
		return nil, e
	}
	t.WorkflowID, t.WorkflowName = wf.ID, wf.Name
	return t, nil
}

// TargetFromRaw digests a raw CWL document.
func TargetFromRaw(rawCWL, toolID string) (*Target, *Error) {
	res, err := toolhash.DigestStandalone(rawCWL, toolID)
	if err != nil {
		return nil, errf(http.StatusBadRequest, "%v", err)
	}
	return &Target{Result: res}, nil
}

// EachRegisteredTool calls fn for every tool of every registered workflow
// (standalone digest). fn returns false to stop.
func EachRegisteredTool(ctx context.Context, st store.Store, fn func(*model.Workflow, *toolhash.StandaloneResult) bool) error {
	const page = model.MaxListLimit
	for offset := 0; ; offset += page {
		wfs, total, err := st.ListWorkflows(ctx, model.ListOptions{Limit: page, Offset: offset})
		if err != nil {
			return err
		}
		for _, wf := range wfs {
			if wf.RawCWL == "" {
				continue
			}
			ids, err := toolhash.ToolIDs(wf.RawCWL)
			if err != nil {
				continue // unparseable registered CWL is not blessable
			}
			for _, id := range ids {
				res, err := toolhash.DigestStandalone(wf.RawCWL, id)
				if err != nil {
					continue
				}
				if !fn(wf, res) {
					return nil
				}
			}
		}
		if offset+page >= total || len(wfs) == 0 {
			return nil
		}
	}
}

// FindToolByHash scans registered workflows for a tool with this digest.
func FindToolByHash(ctx context.Context, st store.Store, hash string) (*Target, error) {
	var found *Target
	err := EachRegisteredTool(ctx, st, func(wf *model.Workflow, res *toolhash.StandaloneResult) bool {
		if res.Hash == hash {
			found = &Target{Result: res, WorkflowID: wf.ID, WorkflowName: wf.Name}
			return false
		}
		return true
	})
	return found, err
}

// PendingTool is a registered token-requesting tool without an active blessing.
type PendingTool struct {
	ToolID       string `json:"tool_id"`
	ToolHash     string `json:"tool_hash"`
	ImageRef     string `json:"image_ref"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
}

// Pending lists registered tools that request the token but are not blessed,
// de-duplicated by hash and sorted by workflow name then tool id.
func Pending(ctx context.Context, st store.Store) ([]PendingTool, error) {
	seen := map[string]bool{}
	out := []PendingTool{}
	var inner error
	err := EachRegisteredTool(ctx, st, func(wf *model.Workflow, res *toolhash.StandaloneResult) bool {
		if !toolhash.ToolRequestsToken(res.Tool) || seen[res.Hash] {
			return true
		}
		b, err := st.GetActiveToolBlessingByHash(ctx, res.Hash)
		if err != nil {
			inner = err
			return false
		}
		if b == nil {
			seen[res.Hash] = true
			out = append(out, PendingTool{res.ToolID, res.Hash, res.ImageRef, wf.ID, wf.Name})
		}
		return true
	})
	if err == nil {
		err = inner
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WorkflowName != out[j].WorkflowName {
			return out[i].WorkflowName < out[j].WorkflowName
		}
		return out[i].ToolID < out[j].ToolID
	})
	return out, nil
}

// HasExecutionUnderRequirements reports whether the tool puts gowe:Execution
// under "requirements" (only "hints" is honoured; one spelling).
func HasExecutionUnderRequirements(tool map[string]any) bool {
	switch r := tool["requirements"].(type) {
	case map[string]any:
		for k := range r {
			if toolhash.IsExecutionClass(k) {
				return true
			}
		}
	case []any:
		for _, it := range r {
			if m, ok := it.(map[string]any); ok {
				if c, _ := m["class"].(string); toolhash.IsExecutionClass(c) {
					return true
				}
			}
		}
	}
	return false
}

var groupNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

var digestRefRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// pinnedImageRef reports whether an image reference names fixed bytes: a local
// .sif file, or a registry reference pinned by digest.
func pinnedImageRef(ref string) bool {
	return strings.HasSuffix(ref, ".sif") || digestRefRe.MatchString(ref)
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseDigests validates and normalises image digests (at least one required).
func ParseDigests(in []string) ([]string, *Error) {
	if len(in) == 0 {
		return nil, errf(http.StatusBadRequest, "image_sha256s is required: a blessing must pin at least one image sha256 digest")
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, d := range in {
		if !model.IsValidSHA256(d) {
			return nil, errf(http.StatusBadRequest, "image_sha256s: %q is not a 64-character hex sha256 digest", d)
		}
		n := model.NormalizeSHA256(d)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out, nil
}

// CreateParams are the administrator-supplied parts of a blessing.
type CreateParams struct {
	Target       *Target
	EnvNames     []string
	WorkerGroups []string
	ImageSHA256s []string
	Note         string
	BlessedBy    string
}

// Create validates and stores a blessing for the target tool.
func Create(ctx context.Context, st store.Store, p CreateParams) (*model.ToolBlessing, *Error) {
	digests, e := ParseDigests(p.ImageSHA256s)
	if e != nil {
		return nil, e
	}
	for _, n := range p.EnvNames {
		if !envNameRe.MatchString(n) {
			return nil, errf(http.StatusBadRequest, "env_names: %q is not a valid environment variable name", n)
		}
	}
	res := p.Target.Result
	if HasExecutionUnderRequirements(res.Tool) {
		return nil, errf(http.StatusBadRequest, "gowe:Execution is declared under requirements; only hints are honoured. Move it to hints and re-register the tool before blessing.")
	}
	if res.ImageRef == "" {
		return nil, errf(http.StatusBadRequest, "the tool declares no DockerRequirement.dockerPull; blessed tools must run in a pinned image")
	}
	if !pinnedImageRef(res.ImageRef) {
		return nil, errf(http.StatusBadRequest, "image %q is not pinned: use a local .sif file or a registry reference pinned as name@sha256:<64 hex>", res.ImageRef)
	}
	for _, g := range p.WorkerGroups {
		if !groupNameRe.MatchString(g) {
			return nil, errf(http.StatusBadRequest, "worker_groups: %q is not a valid group name (letters, digits, '.', '_' or '-')", g)
		}
	}
	b := &model.ToolBlessing{
		ID:           "tb_" + uuid.New().String(),
		ToolHash:     res.Hash,
		ToolName:     p.Target.DisplayName(),
		EnvNames:     p.EnvNames,
		WorkerGroups: p.WorkerGroups,
		ImageRef:     res.ImageRef,
		ImageSHA256s: digests,
		BlessedBy:    p.BlessedBy,
		BlessedAt:    time.Now().UTC(),
		Note:         p.Note,
		Summary:      toolhash.Summarize(res.Tool),
	}
	if err := st.CreateToolBlessing(ctx, b); err != nil {
		if err == store.ErrBlessingActive {
			return nil, errf(http.StatusConflict, "an active blessing for tool hash %s already exists; revoke it first or add an image to it", res.Hash)
		}
		return nil, errf(http.StatusInternalServerError, "%v", err)
	}
	return b, nil
}
