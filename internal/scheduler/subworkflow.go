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

	// Create tasks for each child workflow step.
	for _, step := range childWf.Steps {
		task := &model.Task{
			ID:           "task_" + uuid.New().String(),
			SubmissionID: childSub.ID,
			StepID:       step.ID,
			State:        model.TaskStatePending,
			ExecutorType: model.ExecutorTypeLocal,
			Inputs:       map[string]any{},
			Outputs:      map[string]any{},
			DependsOn:    step.DependsOn,
			MaxRetries:   3,
			CreatedAt:    now,
		}

		// Inherit executor type from step hints.
		if step.Hints != nil {
			if step.Hints.DockerImage != "" {
				task.RuntimeHints = &model.RuntimeHints{
					DockerImage: step.Hints.DockerImage,
				}
			}
			if step.Hints.BVBRCAppID != "" {
				task.BVBRCAppID = step.Hints.BVBRCAppID
			}
			if step.Hints.ExecutorType != "" {
				task.ExecutorType = step.Hints.ExecutorType
			}
		}

		if err := l.store.CreateTask(ctx, task); err != nil {
			return nil, fmt.Errorf("store child task: %w", err)
		}
	}

	l.logger.Info("child submission created",
		"child_sub_id", childSub.ID,
		"parent_task_id", parentTask.ID,
		"workflow", childWf.Name,
		"steps", len(childWf.Steps))

	return childSub, nil
}

// executeChildSubmission synchronously processes all tasks in a child submission.
// Instead of using the global scheduler phases (which would re-dispatch the parent
// task still marked SCHEDULED in the DB), this processes only the child's tasks
// directly in topological order via submitTask().
func (l *Loop) executeChildSubmission(ctx context.Context, childSub *model.Submission) error {
	// Load the child workflow for step definitions and topological order.
	childWf, err := l.store.GetWorkflow(ctx, childSub.WorkflowID)
	if err != nil {
		return fmt.Errorf("get child workflow: %w", err)
	}
	if childWf == nil {
		return fmt.Errorf("child workflow %s not found", childSub.WorkflowID)
	}

	// Compute execution order from step dependencies.
	order := topoSortSteps(childWf.Steps)

	// Process each task in topological order.
	for _, stepID := range order {
		// Reload the task from the store (it may have been created fresh).
		allTasks, err := l.store.ListTasksBySubmission(ctx, childSub.ID)
		if err != nil {
			return fmt.Errorf("list child tasks: %w", err)
		}

		var task *model.Task
		for _, t := range allTasks {
			if t.StepID == stepID {
				task = t
				break
			}
		}
		if task == nil {
			return fmt.Errorf("child task for step %s not found", stepID)
		}

		// Check dependencies before dispatching.
		tasksByStep := BuildTasksByStepID(allTasks)
		_, blocked := AreDependenciesSatisfied(task, tasksByStep)
		if blocked {
			// A dependency failed - skip this task.
			now := time.Now().UTC()
			task.State = model.TaskStateSkipped
			task.CompletedAt = &now
			if err := l.store.UpdateTask(ctx, task); err != nil {
				return fmt.Errorf("skip child task %s: %w", task.ID, err)
			}
			l.logger.Info("child task skipped (dependency blocked)",
				"task_id", task.ID, "step_id", task.StepID)
			continue
		}

		// Mark as SCHEDULED then dispatch via submitTask().
		task.State = model.TaskStateScheduled
		if err := l.store.UpdateTask(ctx, task); err != nil {
			return fmt.Errorf("schedule child task %s: %w", task.ID, err)
		}

		if err := l.submitTask(ctx, task); err != nil {
			l.logger.Error("child submit task failed",
				"task_id", task.ID, "step_id", task.StepID, "error", err)
			// Task state is already updated inside submitTask on failure.
		}

		// Check if task failed (abort early unless other tasks are independent).
		if task.State == model.TaskStateFailed {
			l.logger.Warn("child task failed, continuing with remaining tasks",
				"task_id", task.ID, "step_id", task.StepID)
		}
	}

	// Finalize the child submission: collect outputs and set terminal state.
	// Re-load submission with all tasks to check final states.
	sub, err := l.store.GetSubmission(ctx, childSub.ID)
	if err != nil {
		return fmt.Errorf("reload child submission: %w", err)
	}

	allTerminal := true
	anyFailed := false
	for _, t := range sub.Tasks {
		if !t.State.IsTerminal() {
			allTerminal = false
		}
		if t.State == model.TaskStateFailed {
			anyFailed = true
		}
	}

	if !allTerminal {
		return fmt.Errorf("child submission %s has non-terminal tasks after processing", childSub.ID)
	}

	now := time.Now().UTC()
	if anyFailed {
		sub.State = model.SubmissionStateFailed
	} else {
		sub.State = model.SubmissionStateCompleted

		// Collect workflow outputs.
		outputs, outErr := l.collectWorkflowOutputs(childWf, sub)
		if outErr != nil {
			l.logger.Error("collect child workflow outputs",
				"submission_id", sub.ID, "error", outErr)
		} else {
			sub.Outputs = outputs
		}
	}
	sub.CompletedAt = &now

	if err := l.store.UpdateSubmission(ctx, sub); err != nil {
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

// executeSubWorkflowTask handles a non-scatter sub-workflow step by creating
// a child submission and synchronously executing it.
func (l *Loop) executeSubWorkflowTask(ctx context.Context, task *model.Task,
	step *model.Step, wf *model.Workflow, sub *model.Submission,
	mergedInputs map[string]any, tasksByStep map[string]*model.Task) error {

	l.logger.Info("executing sub-workflow step",
		"task_id", task.ID, "step_id", task.StepID, "tool_ref", step.ToolRef)

	// Parse the parent workflow's CWL to get the sub-workflow graph.
	graphDoc, err := parser.New(l.logger).ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return fmt.Errorf("parse parent CWL: %w", err)
	}

	subGraph := graphDoc.SubWorkflows[step.ToolRef]
	if subGraph == nil {
		return fmt.Errorf("sub-workflow %q not found in parsed graph", step.ToolRef)
	}

	// Create and execute child submission.
	now := time.Now().UTC()
	task.StartedAt = &now

	childSub, err := l.createChildSubmission(ctx, task, subGraph, task.Job, sub, wf)
	if err != nil {
		task.State = model.TaskStateFailed
		task.Stderr = fmt.Sprintf("create child submission: %s", err)
		completedAt := time.Now().UTC()
		task.CompletedAt = &completedAt
		return l.store.UpdateTask(ctx, task)
	}

	// Synchronously execute the child submission.
	if err := l.executeChildSubmission(ctx, childSub); err != nil {
		task.State = model.TaskStateFailed
		task.Stderr = fmt.Sprintf("execute child submission: %s", err)
		completedAt := time.Now().UTC()
		task.CompletedAt = &completedAt
		return l.store.UpdateTask(ctx, task)
	}

	// Propagate child outputs to parent task.
	completedAt := time.Now().UTC()
	if childSub.State == model.SubmissionStateCompleted {
		task.State = model.TaskStateSuccess
		task.Outputs = childSub.Outputs
		exitCode := 0
		task.ExitCode = &exitCode
	} else {
		task.State = model.TaskStateFailed
		task.Stderr = fmt.Sprintf("child submission %s ended with state %s", childSub.ID, childSub.State)
	}
	task.CompletedAt = &completedAt
	task.ExternalID = childSub.ID // Track child submission ID

	l.logger.Info("sub-workflow step completed",
		"task_id", task.ID, "step_id", task.StepID,
		"child_sub_id", childSub.ID, "state", task.State)

	return l.store.UpdateTask(ctx, task)
}

// executeScatterSubWorkflow handles a scatter step over a sub-workflow.
// Each scatter iteration creates a separate child submission.
func (l *Loop) executeScatterSubWorkflow(ctx context.Context, task *model.Task,
	step *model.Step, wf *model.Workflow, sub *model.Submission,
	mergedInputs map[string]any, tasksByStep map[string]*model.Task) error {

	l.logger.Info("executing scatter over sub-workflow",
		"task_id", task.ID, "step_id", task.StepID,
		"scatter", step.Scatter, "method", step.ScatterMethod)

	// Parse the parent workflow's CWL to get the sub-workflow graph.
	graphDoc, err := parser.New(l.logger).ParseGraph([]byte(wf.RawCWL))
	if err != nil {
		return fmt.Errorf("parse parent CWL: %w", err)
	}

	subGraph := graphDoc.SubWorkflows[step.ToolRef]
	if subGraph == nil {
		return fmt.Errorf("sub-workflow %q not found in parsed graph", step.ToolRef)
	}

	// Determine scatter method.
	method := step.ScatterMethod
	if method == "" {
		if len(step.Scatter) == 1 {
			method = "dotproduct"
		} else {
			method = "nested_crossproduct"
		}
	}

	// Extract scatter arrays from the task's resolved job inputs.
	scatterArrays := make(map[string][]any)
	for _, scatterInput := range step.Scatter {
		value := task.Job[scatterInput]
		arr, ok := toAnySlice(value)
		if !ok {
			now := time.Now().UTC()
			task.State = model.TaskStateFailed
			task.Stderr = fmt.Sprintf("scatter input %q is not an array (got %T)", scatterInput, value)
			task.CompletedAt = &now
			return l.store.UpdateTask(ctx, task)
		}
		scatterArrays[scatterInput] = arr
	}

	// Generate input combinations.
	var combinations []map[string]any
	switch method {
	case "dotproduct":
		combinations = scatterDotProduct(task.Job, step.Scatter, scatterArrays)
	case "flat_crossproduct":
		combinations = scatterFlatCrossProduct(task.Job, step.Scatter, scatterArrays)
	case "nested_crossproduct":
		combinations = scatterFlatCrossProduct(task.Job, step.Scatter, scatterArrays)
	default:
		now := time.Now().UTC()
		task.State = model.TaskStateFailed
		task.Stderr = fmt.Sprintf("unknown scatter method: %s", method)
		task.CompletedAt = &now
		return l.store.UpdateTask(ctx, task)
	}

	l.logger.Debug("scatter sub-workflow combinations",
		"task_id", task.ID, "count", len(combinations))

	now := time.Now().UTC()
	task.StartedAt = &now

	// Execute each scatter iteration as a child submission.
	var results []map[string]any
	for i, combo := range combinations {
		// Evaluate 'when' condition per iteration if present.
		if step.When != "" {
			shouldRun, err := l.evaluateWhenForScatterIteration(step, combo, mergedInputs, tasksByStep)
			if err != nil {
				l.logger.Warn("scatter when evaluation failed",
					"task_id", task.ID, "iteration", i, "error", err)
			} else if !shouldRun {
				nullOutputs := make(map[string]any)
				for _, outID := range step.Out {
					nullOutputs[outID] = nil
				}
				results = append(results, nullOutputs)
				continue
			}
		}

		childSub, err := l.createChildSubmission(ctx, task, subGraph, combo, sub, wf)
		if err != nil {
			completedAt := time.Now().UTC()
			task.State = model.TaskStateFailed
			task.Stderr = fmt.Sprintf("scatter iteration %d: create child: %s", i, err)
			task.CompletedAt = &completedAt
			return l.store.UpdateTask(ctx, task)
		}

		if err := l.executeChildSubmission(ctx, childSub); err != nil {
			completedAt := time.Now().UTC()
			task.State = model.TaskStateFailed
			task.Stderr = fmt.Sprintf("scatter iteration %d: execute child: %s", i, err)
			task.CompletedAt = &completedAt
			return l.store.UpdateTask(ctx, task)
		}

		if childSub.State != model.SubmissionStateCompleted {
			completedAt := time.Now().UTC()
			task.State = model.TaskStateFailed
			task.Stderr = fmt.Sprintf("scatter iteration %d: child ended with %s", i, childSub.State)
			task.CompletedAt = &completedAt
			return l.store.UpdateTask(ctx, task)
		}

		results = append(results, childSub.Outputs)
	}

	// Merge results into arrays.
	var mergedOutputs map[string]any
	if method == "nested_crossproduct" && len(step.Scatter) > 1 {
		dims := make([]int, len(step.Scatter))
		for i, name := range step.Scatter {
			dims[i] = len(scatterArrays[name])
		}
		mergedOutputs = mergeScatterResultsNested(results, step.Out, dims)
	} else {
		mergedOutputs = mergeScatterResults(results, step.Out)
	}

	completedAt := time.Now().UTC()
	task.State = model.TaskStateSuccess
	task.Outputs = mergedOutputs
	task.CompletedAt = &completedAt
	exitCode := 0
	task.ExitCode = &exitCode

	l.logger.Info("scatter sub-workflow completed",
		"task_id", task.ID, "step_id", task.StepID,
		"iterations", len(combinations))

	return l.store.UpdateTask(ctx, task)
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

	// Collect all tool entries (CommandLineTool, ExpressionTool) from the parent.
	var tools []any
	var mainWorkflow map[string]any
	for _, item := range graphItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		class, _ := itemMap["class"].(string)
		switch class {
		case "CommandLineTool", "ExpressionTool":
			tools = append(tools, item)
		case "Workflow":
			mainWorkflow = itemMap
		}
	}

	if mainWorkflow == nil {
		return "", fmt.Errorf("no Workflow found in parent $graph")
	}

	// Navigate to the step and extract the inline run: workflow.
	inlineWf, err := extractInlineWorkflow(mainWorkflow, stepID)
	if err != nil {
		return "", fmt.Errorf("extract inline workflow for step %q: %w", stepID, err)
	}

	// Create a copy of the inline workflow with id "main".
	childMainWf := make(map[string]any)
	for k, v := range inlineWf {
		childMainWf[k] = v
	}
	childMainWf["id"] = "main"

	// Build the new $graph: tools + child workflow.
	newGraph := make([]any, 0, len(tools)+1)
	newGraph = append(newGraph, tools...)
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

// extractInlineWorkflow finds a step in a workflow and extracts its inline run: map.
func extractInlineWorkflow(workflow map[string]any, stepID string) (map[string]any, error) {
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

	runVal, ok := stepMap["run"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("step %q run: is not an inline workflow", stepID)
	}

	class, _ := runVal["class"].(string)
	if class != "Workflow" {
		return nil, fmt.Errorf("step %q run: is class %q, not Workflow", stepID, class)
	}

	return runVal, nil
}
