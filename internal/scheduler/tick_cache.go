package scheduler

import (
	"context"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// tickCache provides per-tick memoization to avoid redundant DB reads.
// Reset at the start of each Tick().
type tickCache struct {
	submissions   map[string]*model.Submission
	workflows     map[string]*model.Workflow
	stepInstances map[string][]*model.StepInstance // keyed by submission ID
}

func newTickCache() *tickCache {
	return &tickCache{
		submissions:   make(map[string]*model.Submission),
		workflows:     make(map[string]*model.Workflow),
		stepInstances: make(map[string][]*model.StepInstance),
	}
}

func (tc *tickCache) getSubmission(ctx context.Context, st store.Store, id string) (*model.Submission, error) {
	if sub, ok := tc.submissions[id]; ok {
		return sub, nil
	}
	sub, err := st.GetSubmission(ctx, id)
	if err != nil {
		return nil, err
	}
	tc.submissions[id] = sub
	return sub, nil
}

func (tc *tickCache) getWorkflow(ctx context.Context, st store.Store, id string) (*model.Workflow, error) {
	if wf, ok := tc.workflows[id]; ok {
		return wf, nil
	}
	wf, err := st.GetWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	tc.workflows[id] = wf
	return wf, nil
}

func (tc *tickCache) listStepsBySubmission(ctx context.Context, st store.Store, submissionID string) ([]*model.StepInstance, error) {
	if steps, ok := tc.stepInstances[submissionID]; ok {
		return steps, nil
	}
	steps, err := st.ListStepsBySubmission(ctx, submissionID)
	if err != nil {
		return nil, err
	}
	tc.stepInstances[submissionID] = steps
	return steps, nil
}

// invalidateSteps removes cached step instances for a submission (call after mutation).
func (tc *tickCache) invalidateSteps(submissionID string) {
	delete(tc.stepInstances, submissionID)
}

// invalidateSubmission removes a cached submission (call after mutation).
func (tc *tickCache) invalidateSubmission(submissionID string) {
	delete(tc.submissions, submissionID)
}
