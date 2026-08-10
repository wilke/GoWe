package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// createChildSubmission creates the child submission paired 1:1 with a
// persisted sub-workflow proxy task. It converts the sub-workflow graph to a
// model.Workflow, stores it (deduplicated by content hash), and creates a
// PENDING submission plus WAITING step instances that flow through the normal
// tick machinery like any other submission.
//
// Idempotent per proxy: if a child already exists for this task (dispatch
// re-entered, or repair raced a slow write), it is returned as-is.
//
// INVARIANT: the workflow dedup below (GetWorkflowByHash + CreateWorkflow) is
// race-free only because every dispatch and repair path runs on the single
// scheduler goroutine — do not call this concurrently. [F10]
func (l *Loop) createChildSubmission(ctx context.Context, parentTask *model.Task,
	subGraph *cwl.GraphDocument, inputs map[string]any, parentSub *model.Submission,
	parentWf *model.Workflow) (*model.Submission, error) {

	existing, err := l.store.GetChildSubmissions(ctx, parentTask.ID)
	if err != nil {
		return nil, fmt.Errorf("check existing child submission: %w", err)
	}
	if len(existing) > 0 {
		return existing[0], nil
	}

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
	existingWf, err := l.store.GetWorkflowByHash(ctx, childWf.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("check existing workflow: %w", err)
	}
	if existingWf != nil {
		childWf = existingWf
	} else {
		childWf.ID = "wf_" + uuid.New().String()
		if err := l.store.CreateWorkflow(ctx, childWf); err != nil {
			return nil, fmt.Errorf("store child workflow: %w", err)
		}
	}

	// Child labels: a FRESH map per child. Inheriting the parent's map
	// wholesale would leak a grandparent's parent_task into deeper nesting
	// levels — only the routing/debug labels are inherited, and parent_task
	// is always overwritten per level. [F10]
	labels := map[string]string{"parent_task": parentTask.ID}
	for _, key := range []string{"debug", "worker_group"} {
		if v := parentSub.Labels[key]; v != "" {
			labels[key] = v
		}
	}

	// Create child submission. OutputDestination is deliberately NOT
	// inherited: every child would otherwise upload its shard of outputs to
	// the same workspace destination, overwrite _gowe_outputs.json, and fail
	// the parent on any upload error. The parent uploads the gathered
	// outputs after fan-in, as today. [F6]
	now := time.Now().UTC()
	childSub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   childWf.ID,
		WorkflowName: childWf.Name,
		State:        model.SubmissionStatePending,
		Inputs:       inputs,
		Outputs:      map[string]any{},
		Labels:       labels,
		SubmittedBy:  parentSub.SubmittedBy,
		ParentTaskID: parentTask.ID,
		UserToken:    parentSub.UserToken,
		TokenExpiry:  parentSub.TokenExpiry,
		AuthProvider: parentSub.AuthProvider,
		CreatedAt:    now,
	}

	// StepInstances for each child workflow step, persisted with the
	// submission in ONE transaction: a crash mid-creation can never leave a
	// child submission with zero step instances, which the finalize phase
	// would complete as an (empty) success.
	steps := make([]*model.StepInstance, 0, len(childWf.Steps))
	for _, step := range childWf.Steps {
		steps = append(steps, &model.StepInstance{
			ID:           "si_" + uuid.New().String(),
			SubmissionID: childSub.ID,
			StepID:       step.ID,
			State:        model.StepStateWaiting,
			Outputs:      map[string]any{},
			CreatedAt:    now,
		})
	}
	if err := l.store.CreateSubmissionWithSteps(ctx, childSub, steps); err != nil {
		return nil, fmt.Errorf("store child submission: %w", err)
	}
	l.cache.invalidateSteps(childSub.ID)

	l.logger.Info("child submission created",
		"child_sub_id", childSub.ID,
		"parent_task_id", parentTask.ID,
		"workflow", childWf.Name,
		"steps", len(childWf.Steps))

	return childSub, nil
}

// subWorkflowMarker returns a tool map that marks the task as a sub-workflow.
func subWorkflowMarker(subWfID string) map[string]any {
	return map[string]any{
		"class": "Workflow",
		"id":    subWfID,
	}
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
