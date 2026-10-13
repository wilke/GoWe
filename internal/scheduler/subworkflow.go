package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/toolmerge"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
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
	childRawCWL, err := toolmerge.ChildGraphForStep(parentWf.RawCWL, parentTask.StepID)
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
	//
	// Secrets/SecretNames/SecretsRetention ARE inherited (unlike
	// OutputDestination): a sub-workflow step's tools may need the same
	// secret_env/inject_secrets/cwltool:Secrets access as any other step, and
	// the child submission is the source of truth its own dispatch reads
	// from (mirrors UserToken below) — the proxy task itself carries none,
	// see createSubworkflowProxyTask.
	now := time.Now().UTC()
	childSub := &model.Submission{
		ID:               "sub_" + uuid.New().String(),
		WorkflowID:       childWf.ID,
		WorkflowName:     childWf.Name,
		State:            model.SubmissionStatePending,
		Inputs:           inputs,
		Outputs:          map[string]any{},
		Labels:           labels,
		SubmittedBy:      parentSub.SubmittedBy,
		ParentTaskID:     parentTask.ID,
		UserToken:        parentSub.UserToken,
		TokenExpiry:      parentSub.TokenExpiry,
		AuthProvider:     parentSub.AuthProvider,
		Secrets:          parentSub.Secrets,
		SecretNames:      parentSub.SecretNames,
		SecretsRetention: parentSub.SecretsRetention,
		CreatedAt:        now,
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
