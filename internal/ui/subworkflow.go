package ui

import (
	"context"
	"net/http"

	"github.com/me/gowe/pkg/model"
)

// maxSubworkflowDepth bounds the recursive descendant-count walk (defense in
// depth beside the tree's own acyclic-by-construction shape) — mirrors
// internal/timing.MaxDepth.
const maxSubworkflowDepth = 16

// subworkflowNode is one level of the sub-workflow expansion tree rendered
// by the "subworkflow_children" fragment: a child submission paired 1:1 with
// the sub-workflow proxy task that spawned it (parent_task_id linkage),
// plus enough to render its own task table and, for any of ITS OWN
// sub-workflow proxy tasks, a descendant count so nested rows can show
// "(+N in sub-workflows)" without waiting on a further fetch.
type subworkflowNode struct {
	Submission  *model.Submission
	Tasks       []model.Task
	TaskSummary model.TaskSummary
	// Descendants maps each of this node's own sub-workflow proxy task IDs
	// to its recursive descendant task count (grandchildren and deeper, not
	// including this node's own Tasks).
	Descendants map[string]int
}

// isExpandableSubworkflowProxy reports whether a task is a sub-workflow
// proxy that can be paired with a child submission — excludes when-skip
// synthetic iterations (SUCCESS with no started_at), which the scheduler
// never pairs with a child (see internal/scheduler/loop.go and
// internal/timing's identical check).
func isExpandableSubworkflowProxy(t model.Task) bool {
	if t.ExecutorType != model.ExecutorTypeSubworkflow {
		return false
	}
	if t.State == model.TaskStateSuccess && t.StartedAt == nil {
		return false
	}
	return true
}

// subworkflowDescendantTotal sums a Descendants map's values (Go templates
// cannot reduce a map, so this is precomputed and also used as a template
// func for the same purpose against a node's own map).
func subworkflowDescendantTotal(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

// buildSubworkflowDescendants computes the descendant-count map for a set of
// tasks (typically one submission's own task list): for each sub-workflow
// proxy task in the list, it walks that proxy's child submission tree and
// counts every task in it, recursively. Cost is proportional to the number
// of sub-workflow steps in the tree, not to its total task count, since only
// proxy nodes are ever expanded — the store calls issue.
func (ui *UI) buildSubworkflowDescendants(ctx context.Context, tasks []model.Task) map[string]int {
	counts := map[string]int{}
	for _, t := range tasks {
		if !isExpandableSubworkflowProxy(t) {
			continue
		}
		n, err := ui.subworkflowDescendantCount(ctx, t.ID, 0)
		if err != nil {
			ui.logger.Warn("subworkflow descendant count failed", "task_id", t.ID, "error", err)
			continue
		}
		counts[t.ID] = n
	}
	return counts
}

// subworkflowDescendantCount recursively sums the task count across every
// nested sub-workflow descendant of the given proxy task (not including the
// proxy task itself, nor the task belonging to it — it counts the CHILD
// submission's own tasks plus its descendants).
func (ui *UI) subworkflowDescendantCount(ctx context.Context, proxyTaskID string, depth int) (int, error) {
	if depth >= maxSubworkflowDepth {
		return 0, nil
	}
	kids, err := ui.store.GetChildSubmissions(ctx, proxyTaskID)
	if err != nil {
		return 0, err
	}
	if len(kids) == 0 {
		return 0, nil
	}
	child := kids[0]
	tasks, err := ui.store.ListTasksBySubmission(ctx, child.ID)
	if err != nil {
		return 0, err
	}
	total := len(tasks)
	for _, t := range tasks {
		if !isExpandableSubworkflowProxy(*t) {
			continue
		}
		n, err := ui.subworkflowDescendantCount(ctx, t.ID, depth+1)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// HandleSubmissionTaskChildren serves GET
// /submissions/{id}/tasks/{tid}/children: the lazily-fetched expansion
// fragment for one sub-workflow proxy task, rendering its paired child
// submission's own steps/tasks. Each row is itself expandable the same way
// for grandchildren, fetched on first click via the fragment's own HTMX
// attributes — no upfront N+1 fetch of the whole tree's row data.
//
// {id} is the page's root submission ID, threaded through only to build
// further nested "/submissions/{id}/tasks/{tid}/..." URLs (logs, further
// expansion) — it is NOT an ownership boundary, since a grandchild proxy
// task's own SubmissionID is an intermediate child, not the root. Ownership
// is instead checked against the fetched child submission itself; child
// submissions inherit SubmittedBy from the root submission at creation (see
// internal/scheduler/subworkflow.go), so checking any level is equivalent.
func (ui *UI) HandleSubmissionTaskChildren(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())
	rootID := ui.pathParam(r, "id")
	taskID := ui.pathParam(r, "tid")

	task, err := ui.store.GetTask(r.Context(), taskID)
	if err != nil || task == nil {
		ui.renderNotFound(w, "Task not found")
		return
	}
	if task.ExecutorType != model.ExecutorTypeSubworkflow {
		http.Error(w, "not a sub-workflow task", http.StatusBadRequest)
		return
	}

	kids, err := ui.store.GetChildSubmissions(r.Context(), task.ID)
	if err != nil {
		ui.logger.Error("subworkflow children: failed to load child submission", "task_id", taskID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if len(kids) == 0 {
		// Scheduler hasn't created the child yet (dispatch/child-creation
		// race) or it was since deleted. Render a placeholder rather than
		// erroring — the periodic page refresh will pick it up.
		ui.renderFragment(w, "components/subworkflow_children", map[string]any{
			"RootID":  rootID,
			"Pending": true,
		})
		return
	}
	child := kids[0]

	// Ownership check: non-admin users can only view their own submissions.
	if sess != nil && !sess.IsAdmin() && child.SubmittedBy != sess.Username {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	taskPtrs, err := ui.store.ListTasksBySubmission(r.Context(), child.ID)
	if err != nil {
		ui.logger.Error("subworkflow children: failed to list tasks", "submission_id", child.ID, "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	taskList := make([]model.Task, len(taskPtrs))
	for i, t := range taskPtrs {
		taskList[i] = *t
	}

	node := &subworkflowNode{
		Submission:  child,
		Tasks:       taskList,
		TaskSummary: model.ComputeTaskSummary(taskList),
		Descendants: ui.buildSubworkflowDescendants(r.Context(), taskList),
	}

	ui.renderFragment(w, "components/subworkflow_children", map[string]any{
		"RootID": rootID,
		"Node":   node,
	})
}
