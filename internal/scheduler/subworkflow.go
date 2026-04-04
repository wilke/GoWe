package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
	"gopkg.in/yaml.v3"
)

// isSubWorkflow checks if a tool map represents a CWL Workflow (sub-workflow).
func isSubWorkflow(tool map[string]any) bool {
	if tool == nil {
		return false
	}
	class, ok := tool["class"].(string)
	return ok && class == "Workflow"
}

// createChildSubmission creates a child submission for a sub-workflow step.
// It converts the sub-workflow graph to a model.Workflow, stores it (deduplicated
// by content hash), and creates a submission with tasks for each step.
func (l *Loop) createChildSubmission(ctx context.Context, parentTask *model.Task,
	subGraph *cwl.GraphDocument, inputs map[string]any, parentSub *model.Submission,
	parentWf *model.Workflow) (*model.Submission, error) {

	// Convert sub-workflow graph to model.Workflow.
	p := parser.New(l.logger)
	childWf, err := p.ToModel(subGraph, "sub_"+parentTask.StepID)
	if err != nil {
		return nil, fmt.Errorf("convert sub-workflow to model: %w", err)
	}

	// Build a proper RawCWL for the child that includes the sub-workflow as
	// the main workflow and all tools from the parent graph. Using the parent's
	// RawCWL directly would cause infinite recursion for nested sub-workflows
	// because the parser would find the parent's sub-workflows, not the child's.
	childRawCWL, err := buildChildRawCWL(parentWf.RawCWL, parentTask.StepID)
	if err != nil {
		return nil, fmt.Errorf("build child RawCWL: %w", err)
	}
	childWf.RawCWL = childRawCWL
	childWf.Class = "Workflow"

	// Compute content hash for deduplication.
	hashInput := childWf.RawCWL + "|" + parentTask.StepID
	hash := sha256.Sum256([]byte(hashInput))
	childWf.ContentHash = hex.EncodeToString(hash[:])

	// Check for existing workflow with same content.
	existing, err := l.store.GetWorkflowByHash(ctx, childWf.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("check existing workflow: %w", err)
	}
	if existing != nil {
		childWf = existing
	} else {
		childWf.ID = "wf_" + uuid.New().String()
		if err := l.store.CreateWorkflow(ctx, childWf); err != nil {
			return nil, fmt.Errorf("store child workflow: %w", err)
		}
	}

	// Create child submission.
	now := time.Now().UTC()
	childSub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   childWf.ID,
		WorkflowName: childWf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       map[string]string{"parent_task": parentTask.ID},
		SubmittedBy:  parentSub.SubmittedBy,
		ParentTaskID: parentTask.ID,
		UserToken:    parentSub.UserToken,
		TokenExpiry:  parentSub.TokenExpiry,
		AuthProvider: parentSub.AuthProvider,
		CreatedAt:    now,
	}

	if err := l.store.CreateSubmission(ctx, childSub); err != nil {
		return nil, fmt.Errorf("store child submission: %w", err)
	}

	// Create StepInstances for each child workflow step.
	for _, step := range childWf.Steps {
		si := &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: childSub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		}

		if err := l.store.CreateStepInstance(ctx, si); err != nil {
			return nil, fmt.Errorf("store child step instance: %w", err)
		}
	}
	l.cache.invalidateSteps(childSub.ID)

	l.logger.Info("child submission created",
		"child_sub_id", childSub.ID,
		"parent_task_id", parentTask.ID,
		"workflow", childWf.Name,
		"steps", len(childWf.Steps))

	return childSub, nil
}

// executeChildSubmission synchronously processes all steps in a child submission.
// It iterates through StepInstances in topological order, dispatching each step
// via the standard submitStep flow.
func (l *Loop) executeChildSubmission(ctx context.Context, childSub *model.Submission) error {
	// Load the child workflow for step definitions and topological order.
	childWf, err := l.cache.getWorkflow(ctx, l.store, childSub.WorkflowID)
	if err != nil {
		return fmt.Errorf("get child workflow: %w", err)
	}
	if childWf == nil {
		return fmt.Errorf("child workflow %s not found", childSub.WorkflowID)
	}

	// Compute execution order from step dependencies.
	order := topoSortSteps(childWf.Steps)

	// Build step dependency map for checking readiness.
	stepDeps := make(map[string][]string)
	for _, s := range childWf.Steps {
		stepDeps[s.ID] = s.DependsOn
	}

	// Process each step instance in topological order.
	for _, stepID := range order {
		// Load all step instances for this submission.
		allSteps, err := l.cache.listStepsBySubmission(ctx, l.store, childSub.ID)
		if err != nil {
			return fmt.Errorf("list child step instances: %w", err)
		}

		var si *model.StepInstance
		stepByID := make(map[string]*model.StepInstance)
		for _, s := range allSteps {
			stepByID[s.StepID] = s
			if s.StepID == stepID {
				si = s
			}
		}
		if si == nil {
			return fmt.Errorf("child step instance for step %s not found", stepID)
		}

		// Check dependencies using step-level tracking.
		blocked := false
		for _, depStepID := range stepDeps[stepID] {
			dep, ok := stepByID[depStepID]
			if !ok {
				blocked = true
				break
			}
			switch dep.State {
			case model.StepStateFailed, model.StepStateSkipped:
				blocked = true
			case model.StepStateCompleted:
				continue
			default:
				// Should not happen in synchronous execution
				blocked = true
			}
			if blocked {
				break
			}
		}

		if blocked {
			now := time.Now().UTC()
			si.State = model.StepStateSkipped
			si.CompletedAt = &now
			if err := l.updateStepInstance(ctx, si); err != nil {
				return fmt.Errorf("skip child step %s: %w", si.ID, err)
			}
			l.logger.Info("child step skipped (dependency blocked)",
				"si_id", si.ID, "step_id", si.StepID)
			continue
		}

		// Dispatch this step: transition WAITING → READY → dispatch.
		si.State = model.StepStateReady
		if err := l.updateStepInstance(ctx, si); err != nil {
			return fmt.Errorf("ready child step %s: %w", si.ID, err)
		}

		if err := l.dispatchStep(ctx, si, childWf, childSub); err != nil {
			l.logger.Error("child dispatch step failed",
				"si_id", si.ID, "step_id", si.StepID, "error", err)
		}

		// Reload step instance to check final state.
		si, err = l.store.GetStepInstance(ctx, si.ID)
		if err != nil {
			return fmt.Errorf("reload child step %s: %w", si.ID, err)
		}

		// If step is async (DISPATCHED/RUNNING), poll until all its tasks complete.
		if si.State == model.StepStateDispatched || si.State == model.StepStateRunning {
			if err := l.waitForStepCompletion(ctx, si, childWf); err != nil {
				return fmt.Errorf("wait for child step %s: %w", si.ID, err)
			}
			si, err = l.store.GetStepInstance(ctx, si.ID)
			if err != nil {
				return fmt.Errorf("reload completed step %s: %w", si.ID, err)
			}
		}

		if si.State == model.StepStateFailed {
			l.logger.Warn("child step failed, continuing with remaining steps",
				"si_id", si.ID, "step_id", si.StepID)
		}
	}

	// Finalize the child submission.
	allSteps, err := l.cache.listStepsBySubmission(ctx, l.store, childSub.ID)
	if err != nil {
		return fmt.Errorf("reload child steps: %w", err)
	}

	allTerminal := true
	anyFailed := false
	for _, si := range allSteps {
		if !si.State.IsTerminal() {
			allTerminal = false
		}
		if si.State == model.StepStateFailed {
			anyFailed = true
		}
	}

	if !allTerminal {
		return fmt.Errorf("child submission %s has non-terminal steps after processing", childSub.ID)
	}

	// Collect workflow outputs from step instances.
	stepOutputs := make(map[string]map[string]any)
	for _, si := range allSteps {
		if si.Outputs != nil {
			stepOutputs[si.StepID] = si.Outputs
		}
	}

	now := time.Now().UTC()
	sub := &model.Submission{
		ID:           childSub.ID,
		WorkflowID:   childSub.WorkflowID,
		WorkflowName: childSub.WorkflowName,
		State:        childSub.State,
		Inputs:       childSub.Inputs,
		Outputs:      childSub.Outputs,
		Labels:       childSub.Labels,
		SubmittedBy:  childSub.SubmittedBy,
		ParentTaskID: childSub.ParentTaskID,
		CreatedAt:    childSub.CreatedAt,
	}

	if anyFailed {
		sub.State = model.SubmissionStateFailed
		sub.Error = l.buildSubmissionError(ctx, allSteps)
	} else {
		sub.State = model.SubmissionStateCompleted

		// Collect workflow outputs using step instance outputs.
		outputs, outErr := l.collectWorkflowOutputsFromSteps(childWf, stepOutputs, childSub.Inputs)
		if outErr != nil {
			l.logger.Error("collect child workflow outputs",
				"submission_id", sub.ID, "error", outErr)
		} else {
			sub.Outputs = outputs
		}
	}
	sub.CompletedAt = &now

	if err := l.updateSubmission(ctx, sub); err != nil {
		return fmt.Errorf("finalize child submission: %w", err)
	}

	// Propagate results back to caller.
	childSub.State = sub.State
	childSub.Outputs = sub.Outputs
	childSub.CompletedAt = sub.CompletedAt

	l.logger.Info("child submission finalized",
		"child_sub_id", childSub.ID, "state", sub.State)

	return nil
}

// waitForStepCompletion polls an async step's tasks until all are terminal,
// then updates the step instance state. This mirrors the pollInFlight + advanceSteps
// logic from the main scheduler loop but scoped to a single step.
//
// NOTE: This runs synchronously inside Phase 2 (dispatchReady), blocking the
// main scheduler loop. Use a slower poll interval than the main scheduler to
// reduce DB contention, and enforce a timeout to prevent infinite blocking.
func (l *Loop) waitForStepCompletion(ctx context.Context, si *model.StepInstance, wf *model.Workflow) error {
	// Use a slower poll interval for child steps (min 500ms) to reduce DB load
	// and give workers more opportunity to check out tasks.
	pollInterval := l.config.PollInterval
	if pollInterval < 500*time.Millisecond {
		pollInterval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Safety timeout: 10 minutes per child step to prevent infinite blocking.
	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("child step %s timed out after 10 minutes", si.ID)
		case <-ticker.C:
			tasks, err := l.store.ListTasksByStepInstance(ctx, si.ID)
			if err != nil {
				return fmt.Errorf("list tasks for step %s: %w", si.ID, err)
			}

			// Poll each non-terminal task via its executor.
			for _, task := range tasks {
				if task.State.IsTerminal() {
					continue
				}
				exec, err := l.registry.Get(task.ExecutorType)
				if err != nil {
					l.logger.Error("get executor for child poll", "task_id", task.ID, "error", err)
					continue
				}
				newState, err := exec.Status(ctx, task)
				if err != nil {
					l.logger.Error("poll child task status", "task_id", task.ID, "error", err)
					continue
				}
				if newState == task.State {
					continue
				}
				task.State = newState
				if newState == model.TaskStateRunning && task.StartedAt == nil {
					now := time.Now().UTC()
					task.StartedAt = &now
				}
				if newState.IsTerminal() {
					now := time.Now().UTC()
					task.CompletedAt = &now
					stdout, stderr, _ := exec.Logs(ctx, task)
					task.Stdout = stdout
					task.Stderr = stderr
				}
				if err := l.store.UpdateTask(ctx, task); err != nil {
					l.logger.Error("update child polled task", "task_id", task.ID, "error", err)
				}
			}

			// Re-fetch tasks to check terminal state.
			tasks, err = l.store.ListTasksByStepInstance(ctx, si.ID)
			if err != nil {
				return fmt.Errorf("re-list tasks for step %s: %w", si.ID, err)
			}

			allTerminal := true
			anyFailed := false
			for _, t := range tasks {
				if !t.State.IsTerminal() {
					allTerminal = false
					break
				}
				if t.State == model.TaskStateFailed {
					anyFailed = true
				}
			}

			if !allTerminal {
				continue
			}

			// All tasks terminal — update step instance.
			now := time.Now().UTC()
			if anyFailed {
				si.State = model.StepStateFailed
			} else {
				si.State = model.StepStateCompleted
				// Merge outputs: scatter vs single task.
				if si.ScatterCount > 0 {
					step := findStep(wf, si.StepID)
					if step != nil {
						results := make([]map[string]any, len(tasks))
						for _, t := range tasks {
							if t.ScatterIndex >= 0 && t.ScatterIndex < len(results) {
								results[t.ScatterIndex] = t.Outputs
							}
						}
						if si.ScatterMethod == "nested_crossproduct" && len(si.ScatterDims) > 1 {
							si.Outputs = mergeScatterResultsNested(results, step.Out, si.ScatterDims)
						} else {
							si.Outputs = mergeScatterResults(results, step.Out)
						}
					}
				} else if len(tasks) == 1 {
					si.Outputs = tasks[0].Outputs
				}
			}
			si.CompletedAt = &now
			if err := l.updateStepInstance(ctx, si); err != nil {
				return fmt.Errorf("complete child step %s: %w", si.ID, err)
			}
			l.logger.Info("child step completed (poll)",
				"si_id", si.ID, "step_id", si.StepID, "state", si.State)
			return nil
		}
	}
}

// topoSortSteps returns step IDs in topological order using Kahn's algorithm.
func topoSortSteps(steps []model.Step) []string {
	inDegree := make(map[string]int, len(steps))
	forward := make(map[string][]string, len(steps))

	for _, s := range steps {
		if _, ok := inDegree[s.ID]; !ok {
			inDegree[s.ID] = 0
		}
		for _, dep := range s.DependsOn {
			forward[dep] = append(forward[dep], s.ID)
			inDegree[s.ID]++
		}
	}

	var queue, order []string
	for _, s := range steps {
		if inDegree[s.ID] == 0 {
			queue = append(queue, s.ID)
		}
	}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		for _, succ := range forward[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
	}

	return order
}

// subWorkflowMarker returns a tool map that marks the task as a sub-workflow.
func subWorkflowMarker(subWfID string) map[string]any {
	return map[string]any{
		"class": "Workflow",
		"id":    subWfID,
	}
}

// resolveSubWorkflowJob resolves inputs for a sub-workflow step (same as tool steps).
func resolveSubWorkflowJob(tool map[string]any) string {
	if id, ok := tool["id"].(string); ok {
		return id
	}
	return ""
}

// marshalForHash serializes inputs to JSON for content hashing.
func marshalForHash(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// buildChildRawCWL constructs a new $graph YAML document for a child submission.
// It extracts the inline sub-workflow from the parent step and combines it with
// all tool definitions from the parent graph. This ensures that:
// 1. The child sees the sub-workflow's own inline workflows (not the parent's)
// 2. All tools referenced by #fragment are available in the child's graph
func buildChildRawCWL(parentRawCWL string, stepID string) (string, error) {
	var parentDoc map[string]any
	if err := yaml.Unmarshal([]byte(parentRawCWL), &parentDoc); err != nil {
		return "", fmt.Errorf("parse parent CWL: %w", err)
	}

	graphItems, ok := parentDoc["$graph"].([]any)
	if !ok {
		return "", fmt.Errorf("parent CWL has no $graph")
	}

	// Index all graph items by ID and collect non-main entries.
	graphByID := make(map[string]map[string]any)
	var mainWorkflow map[string]any
	var nonMainItems []any
	for _, item := range graphItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := itemMap["id"].(string)
		if id != "" {
			graphByID[id] = itemMap
		}
		class, _ := itemMap["class"].(string)
		if class == "Workflow" && id == "main" {
			mainWorkflow = itemMap
		} else {
			nonMainItems = append(nonMainItems, item)
		}
	}

	if mainWorkflow == nil {
		return "", fmt.Errorf("no main Workflow found in parent $graph")
	}

	// Find the sub-workflow for the given step.
	// It may be inline (run: {class: Workflow, ...}) or a fragment reference (run: "#id").
	subWf, err := extractStepWorkflow(mainWorkflow, stepID, graphByID)
	if err != nil {
		return "", fmt.Errorf("extract workflow for step %q: %w", stepID, err)
	}

	// Create a copy of the sub-workflow with id "main".
	childMainWf := make(map[string]any)
	for k, v := range subWf {
		childMainWf[k] = v
	}
	childMainWf["id"] = "main"

	// Build the new $graph: all non-main items + child workflow as main.
	// This includes tools and any other sub-workflows needed by the child.
	newGraph := make([]any, 0, len(nonMainItems)+1)
	// Add non-main items, excluding the sub-workflow itself (it becomes main).
	subWfID, _ := subWf["id"].(string)
	for _, item := range nonMainItems {
		if itemMap, ok := item.(map[string]any); ok {
			if id, _ := itemMap["id"].(string); id == subWfID {
				continue // Skip — it's becoming main.
			}
		}
		newGraph = append(newGraph, item)
	}
	newGraph = append(newGraph, childMainWf)

	newDoc := map[string]any{
		"cwlVersion": parentDoc["cwlVersion"],
		"$graph":     newGraph,
	}

	out, err := yaml.Marshal(newDoc)
	if err != nil {
		return "", fmt.Errorf("marshal child CWL: %w", err)
	}
	return string(out), nil
}

// extractStepWorkflow finds a step in a workflow and returns its sub-workflow definition.
// Handles both inline workflows (run: {class: Workflow, ...}) and fragment references
// (run: "#count-lines1-wf") by looking up the reference in graphByID.
func extractStepWorkflow(workflow map[string]any, stepID string, graphByID map[string]map[string]any) (map[string]any, error) {
	// Steps can be map or array format.
	var steps map[string]any
	switch s := workflow["steps"].(type) {
	case map[string]any:
		steps = s
	case []any:
		steps = make(map[string]any)
		for _, item := range s {
			if stepMap, ok := item.(map[string]any); ok {
				if id, ok := stepMap["id"].(string); ok {
					id = strings.TrimPrefix(id, "#")
					if idx := strings.LastIndex(id, "/"); idx >= 0 {
						id = id[idx+1:]
					}
					steps[id] = stepMap
				}
			}
		}
	default:
		return nil, fmt.Errorf("workflow has no steps")
	}

	stepVal, ok := steps[stepID]
	if !ok {
		return nil, fmt.Errorf("step %q not found in workflow", stepID)
	}
	stepMap, ok := stepVal.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("step %q is not a map", stepID)
	}

	// Handle inline workflow (run: {class: Workflow, ...}).
	if runVal, ok := stepMap["run"].(map[string]any); ok {
		class, _ := runVal["class"].(string)
		if class != "Workflow" {
			return nil, fmt.Errorf("step %q run: is class %q, not Workflow", stepID, class)
		}
		return runVal, nil
	}

	// Handle fragment reference (run: "#count-lines1-wf").
	if runRef, ok := stepMap["run"].(string); ok {
		ref := strings.TrimPrefix(runRef, "#")
		if wf, ok := graphByID[ref]; ok {
			class, _ := wf["class"].(string)
			if class != "Workflow" {
				return nil, fmt.Errorf("step %q run: references %q which is class %q, not Workflow", stepID, ref, class)
			}
			return wf, nil
		}
		return nil, fmt.Errorf("step %q run: references %q which is not in $graph", stepID, ref)
	}

	return nil, fmt.Errorf("step %q run: is neither an inline workflow nor a fragment reference", stepID)
}
