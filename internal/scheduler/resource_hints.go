package scheduler

import (
	"log/slog"

	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// resourceHinter turns a tool's ResourceRequirement (static values or
// expressions over the job) into RuntimeHints.Cores / RamMB so the scheduler
// and CheckoutTask can gate on worker capacity (#324). It evaluates the final
// merged task.Tool, so step- and workflow-level requirements and the CWL
// precedence rules are honoured exactly as the worker will see them.
type resourceHinter struct {
	tool   *cwl.CommandLineTool
	lib    []string
	logger *slog.Logger
	siID   string
	warned bool
}

// newResourceHinter returns nil (a no-op hinter) when tmpTask has no
// CommandLineTool, the tool does not parse, or it declares no
// ResourceRequirement.
func (l *Loop) newResourceHinter(si *model.StepInstance, tmpTask *model.Task) *resourceHinter {
	if tmpTask == nil || tmpTask.Tool == nil {
		return nil
	}
	if class, _ := tmpTask.Tool["class"].(string); class != "" && class != "CommandLineTool" {
		return nil
	}
	if !toolDeclaresResourceRequirement(tmpTask.Tool) {
		return nil
	}
	tool, err := parser.New(l.logger).ParseToolFromMap(tmpTask.Tool)
	if err != nil || tool == nil {
		if err != nil {
			l.logger.Debug("resource hints: tool does not parse", "si_id", si.ID, "error", err)
		}
		return nil
	}
	var lib []string
	if tmpTask.RuntimeHints != nil {
		lib = tmpTask.RuntimeHints.ExpressionLib
	}
	return &resourceHinter{tool: tool, lib: lib, logger: l.logger, siID: si.ID}
}

func toolDeclaresResourceRequirement(tool map[string]any) bool {
	for _, section := range []string{"requirements", "hints"} {
		if m, ok := tool[section].(map[string]any); ok {
			if _, has := m["ResourceRequirement"]; has {
				return true
			}
		}
	}
	return false
}

// evaluate returns the resource request for one job. It never fails: a
// dimension that cannot be evaluated is 0 (no constraint) and the first
// failure per step instance is logged at WARN. A nil hinter returns zero.
func (h *resourceHinter) evaluate(job map[string]any) cwltool.ResourceRequest {
	if h == nil {
		return cwltool.ResourceRequest{}
	}
	req, err := cwltool.EvaluateResourceRequest(h.tool, job, h.lib)
	if err != nil && !h.warned {
		h.warned = true
		h.logger.Warn("resource requirement not evaluable at dispatch; capacity gating skipped for it",
			"si_id", h.siID, "error", err)
	}
	return req
}

// evaluateAll evaluates one request per scatter combination, in order, so the
// JS evaluation happens once per iteration and the same values feed both the
// preflight and the tasks (applyRequest).
func (h *resourceHinter) evaluateAll(jobs []map[string]any) []cwltool.ResourceRequest {
	reqs := make([]cwltool.ResourceRequest, len(jobs))
	if h == nil {
		return reqs
	}
	for i, job := range jobs {
		reqs[i] = h.evaluate(job)
	}
	return reqs
}

// applyRequest stores req in a private copy of task.RuntimeHints (scatter
// tasks share the template's pointer). A nil hinter leaves the task untouched.
func (h *resourceHinter) applyRequest(task *model.Task, req cwltool.ResourceRequest) {
	if h == nil || task == nil {
		return
	}
	task.RuntimeHints = cloneRuntimeHints(task.RuntimeHints)
	task.RuntimeHints.Cores = req.Cores
	task.RuntimeHints.RamMB = req.RamMB
}

// apply evaluates the resource request against task.Job and stores it.
func (h *resourceHinter) apply(task *model.Task) {
	if h == nil || task == nil {
		return
	}
	h.applyRequest(task, h.evaluate(task.Job))
}
