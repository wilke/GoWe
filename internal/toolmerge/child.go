package toolmerge

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ChildGraphForStep constructs a new $graph YAML document for a child submission.
// It extracts the inline sub-workflow from the parent step and combines it with
// all tool definitions from the parent graph. This ensures that:
// 1. The child sees the sub-workflow's own inline workflows (not the parent's)
// 2. All tools referenced by #fragment are available in the child's graph
func ChildGraphForStep(parentRawCWL string, stepID string) (string, error) {
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
