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

// apply evaluates the resource request against task.Job and stores it in a
// private copy of task.RuntimeHints (scatter tasks share the template's
// pointer). It never fails: an evaluation failure leaves that dimension 0
// (no constraint) and logs one WARN per step instance.
func (h *resourceHinter) apply(task *model.Task) { h.applyTo(task, true) }

// applyTemplate is apply for the step's template task, whose scatter inputs
// are still whole arrays: failures are expected there and not logged.
func (h *resourceHinter) applyTemplate(task *model.Task) { h.applyTo(task, false) }

func (h *resourceHinter) applyTo(task *model.Task, warn bool) {
	if h == nil || task == nil {
		return
	}
	req, err := cwltool.EvaluateResourceRequest(h.tool, task.Job, h.lib)
	if err != nil && warn && !h.warned {
		h.warned = true
		h.logger.Warn("resource requirement not evaluable at dispatch; capacity gating skipped for it",
			"si_id", h.siID, "step_id", task.StepID, "error", err)
	}
	task.RuntimeHints = cloneRuntimeHints(task.RuntimeHints)
	task.RuntimeHints.Cores = req.Cores
	task.RuntimeHints.RamMB = req.RamMB
}
