package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/internal/toolmerge"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// Error codes of the submit-time token-policy check.
const (
	// ErrToolNotBlessed: a step's tool requests the user's token but no
	// active blessing covers its exact post-merge content (or its worker group).
	ErrToolNotBlessed model.ErrorCode = "TOOL_NOT_BLESSED"
	// ErrTokenToolOverrideRefused: the submission tries to alter a
	// token-requesting tool (job cwl:requirements, step-level image settings).
	ErrTokenToolOverrideRefused model.ErrorCode = "TOKEN_TOOL_OVERRIDE_REFUSED"
)

// Token-blessing modes (mirrors scheduler.TokenBlessing*).
const (
	tokenBlessingEnforce = "enforce"
	tokenBlessingDev     = "dev"
)

// WithTokenBlessingMode sets the --token-blessing mode ("enforce", the
// default, or "dev"). Dev mode only downgrades an unblessed token request from
// a 400 to a response warning; blessed tools behave identically in both.
func WithTokenBlessingMode(mode string) Option {
	return func(s *Server) {
		if mode == tokenBlessingDev {
			s.tokenBlessing = tokenBlessingDev
		} else {
			s.tokenBlessing = tokenBlessingEnforce
		}
	}
}

func (s *Server) tokenBlessingMode() string {
	if s.tokenBlessing == tokenBlessingDev {
		return tokenBlessingDev
	}
	return tokenBlessingEnforce
}

// tokenFinding is one step whose tool requests the token.
type tokenFinding struct {
	step      string // path, e.g. "cga/annotate"
	tool      string
	hash      string
	inherited []string
	reason    string // human text for this finding
}

func (f tokenFinding) detail() model.FieldError {
	msg := fmt.Sprintf("step %q (tool %q, tool_hash %s): %s", f.step, f.tool, f.hash, f.reason)
	if len(f.inherited) > 0 {
		msg += fmt.Sprintf(" [inherited_non_benign: %s]", strings.Join(f.inherited, ", "))
	}
	return model.FieldError{Field: f.step, Path: f.hash, Message: msg}
}

// checkTokenRequests is the submit-time fast-fail of the provider-token policy
// (dispatch in the scheduler stays authoritative). For every step, recursing
// into sub-workflows on the CHILD's raw CWL exactly as the scheduler builds it
// (toolmerge.ChildGraphForStep), whose tool requests the token it:
//
//   - refuses job cwl:requirements and step-level image settings
//     (DockerRequirement or gowe:Execution.docker_image under requirements OR
//     hints): TOKEN_TOOL_OVERRIDE_REFUSED;
//   - computes the digest the scheduler will compute (DigestForStep with the
//     ACTUAL job cwl:requirements) and requires an active blessing that
//     allows the effective worker group: TOOL_NOT_BLESSED. In dev mode an
//     unblessed tool is accepted and returned as a warning instead.
//
// Nested steps get nil job requirements: createChildSubmission passes the
// proxy task's resolved job as the child's inputs, which never carries the
// parent submission's cwl:requirements, so nothing from the top-level job can
// reach a nested step at dispatch. Refusing top-level cwl:requirements only
// for top-level token steps keeps dry run and dispatch identical.
//
// It returns a non-nil APIError to reject the submission, and warnings to
// attach to the accepted response.
func (s *Server) checkTokenRequests(ctx context.Context, wf *model.Workflow, inputs map[string]any, labels map[string]string, hasUserToken bool) (*model.APIError, []model.FieldError) {
	if wf == nil || wf.RawCWL == "" {
		return nil, nil
	}
	var overrides, unblessed, invalid []tokenFinding
	var warnings []model.FieldError

	jobReqs, _ := inputs["cwl:requirements"].([]any)
	jobReqsPresent := inputs["cwl:requirements"] != nil
	if l, ok := inputs["cwl:requirements"].([]any); ok && len(l) == 0 {
		jobReqsPresent = false // an empty list overrides nothing
	}

	var walk func(raw, prefix string, depth int)
	walk = func(raw, prefix string, depth int) {
		graph, err := s.parser.ParseGraph([]byte(raw))
		if err != nil || graph.Workflow == nil {
			s.logger.Debug("token check: cannot parse CWL; dispatch will decide", "prefix", prefix, "error", err)
			return
		}
		// Merged step hints (tool + step, field-wise) give the effective worker group.
		groups := map[string]string{}
		if mw, err := s.parser.ToModel(graph, "token-check"); err == nil {
			for _, st := range mw.Steps {
				if st.Hints != nil {
					groups[st.ID] = st.Hints.WorkerGroup
				}
			}
		}

		ids := make([]string, 0, len(graph.Workflow.Steps))
		for id := range graph.Workflow.Steps {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			step := graph.Workflow.Steps[id]
			path := prefix + id
			runID := strings.TrimPrefix(step.Run, "#")

			if _, isSub := graph.SubWorkflows[runID]; isSub {
				child, err := toolmerge.ChildGraphForStep(raw, id)
				if err != nil {
					s.logger.Debug("token check: cannot build child root", "step", path, "error", err)
					continue
				}
				walk(child, path+"/", depth+1)
				continue
			}
			if !hasCommandLineTool(graph, runID) {
				continue
			}

			var reqs []any
			if depth == 0 {
				reqs = jobReqs
			}
			res, err := toolhash.DigestForStep(raw, id, reqs)
			if err != nil {
				s.logger.Debug("token check: cannot digest step", "step", path, "error", err)
				continue
			}
			if !toolhash.ToolRequestsToken(res.Tool) {
				continue
			}
			f := tokenFinding{step: path, tool: res.ToolID, hash: res.Hash, inherited: res.Inherited}

			if !hasUserToken {
				o := f
				o.reason = "requests the user's token but this submission carries none (anonymous or token-less authentication); authenticate with a provider token"
				invalid = append(invalid, o)
			}
			if res.ImageRef == "" {
				o := f
				o.reason = "requests the user's token but declares no container image (DockerRequirement.dockerPull); only tools in a pinned image can be blessed"
				invalid = append(invalid, o)
			}

			if depth == 0 && jobReqsPresent {
				o := f
				o.reason = "job-level cwl:requirements are not allowed with a token-requesting step"
				overrides = append(overrides, o)
			}
			if what := stepImageSettings(step); len(what) > 0 {
				o := f
				o.reason = "step-level image settings are not allowed on a token-requesting step (" + strings.Join(what, ", ") + ")"
				overrides = append(overrides, o)
			}

			b, err := s.store.GetActiveToolBlessingByHash(ctx, res.Hash)
			if err != nil {
				s.logger.Error("token check: blessing lookup failed", "step", path, "error", err)
				f.reason = "could not look up the blessing: " + err.Error()
				unblessed = append(unblessed, f)
				continue
			}
			switch {
			case b == nil && s.tokenBlessingMode() == tokenBlessingDev:
				w := f
				w.reason = "requests the user's token and is NOT blessed; accepted because the server runs with --token-blessing=dev"
				warnings = append(warnings, w.detail())
			case b == nil:
				f.reason = "requests the user's token but is not blessed (ask an administrator to run `gowe admin bless`)"
				if len(f.inherited) > 0 {
					f.reason = fmt.Sprintf("the registered tool may be blessed, but this workflow adds %s to it, which changes the blessed content; blessed tools must declare their own requirements",
						strings.Join(f.inherited, ", "))
				}
				unblessed = append(unblessed, f)
			default:
				group := groups[id]
				if group == "" {
					group = labels["worker_group"]
				}
				if group == "" {
					group = "default"
				}
				if !b.AllowsGroup(group) {
					f.reason = fmt.Sprintf("is blessed (%s) but not for worker group %q (allowed: %s)", b.ID, group, strings.Join(b.WorkerGroups, ", "))
					unblessed = append(unblessed, f)
				}
			}
		}
	}
	walk(wf.RawCWL, "", 0)

	if len(invalid) > 0 {
		details := make([]model.FieldError, len(invalid))
		for i, f := range invalid {
			details[i] = f.detail()
		}
		return model.NewValidationError("submission refused: "+details[0].Message, details...), nil
	}
	if len(overrides) > 0 {
		details := make([]model.FieldError, len(overrides))
		for i, f := range overrides {
			details[i] = f.detail()
		}
		return &model.APIError{
			Code:    ErrTokenToolOverrideRefused,
			Message: "submission refused: " + details[0].Message,
			Details: details,
		}, nil
	}
	if len(unblessed) > 0 {
		details := make([]model.FieldError, len(unblessed))
		for i, f := range unblessed {
			details[i] = f.detail()
		}
		return &model.APIError{
			Code:    ErrToolNotBlessed,
			Message: "submission refused: " + details[0].Message,
			Details: details,
		}, nil
	}
	return nil, warnings
}

func hasCommandLineTool(graph *cwl.GraphDocument, id string) bool {
	for tid := range graph.Tools {
		if strings.TrimPrefix(tid, "#") == id || tid == id {
			return true
		}
	}
	return false
}

// stepImageSettings lists step-level image overrides: a DockerRequirement or
// gowe:Execution.docker_image under the step's requirements or hints.
func stepImageSettings(step cwl.Step) []string {
	var out []string
	for _, sect := range []struct {
		name string
		m    map[string]any
	}{{"requirements", step.Requirements}, {"hints", step.Hints}} {
		for class, body := range sect.m {
			if class == "DockerRequirement" {
				out = append(out, "DockerRequirement under "+sect.name)
			}
			if toolhash.IsExecutionClass(class) {
				if m, ok := body.(map[string]any); ok {
					if v, _ := m["docker_image"].(string); v != "" {
						out = append(out, "gowe:Execution.docker_image under "+sect.name)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// unblessedTokenToolWarnings returns registration-time warnings for tools in a
// freshly parsed document that request the token but have no active blessing.
// Non-fatal: blessing is checked again at submit and at dispatch.
func (s *Server) unblessedTokenToolWarnings(ctx context.Context, rawCWL string) []string {
	ids, err := toolhash.ToolIDs(rawCWL)
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		res, err := toolhash.DigestStandalone(rawCWL, id)
		if err != nil || !toolhash.ToolRequestsToken(res.Tool) {
			continue
		}
		b, err := s.store.GetActiveToolBlessingByHash(ctx, res.Hash)
		if err == nil && b != nil {
			continue
		}
		out = append(out, fmt.Sprintf("tool %q requests the user's token but is not blessed (tool_hash %s); submissions of workflows using it will be refused until an administrator blesses it",
			id, res.Hash))
	}
	return out
}
