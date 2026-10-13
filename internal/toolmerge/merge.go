// Package toolmerge holds the pure functions that turn a registered tool plus
// its surrounding workflow/job context into the final post-merge tool document
// a worker executes. The scheduler, the tool-blessing hash (internal/toolhash)
// and the admin/inspect handlers all call the same code so that bless-time,
// submit-time and dispatch-time views of a tool can never disagree.
package toolmerge

import (
	"encoding/json"
	"fmt"

	"github.com/me/gowe/pkg/cwl"
)

// StructToToolMap converts a parsed tool struct into the generic map form the
// scheduler stores in task.Tool (struct -> JSON -> map round trip).
func StructToToolMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal tool: %w", err)
	}
	var tool map[string]any
	if err := json.Unmarshal(data, &tool); err != nil {
		return nil, fmt.Errorf("unmarshal tool: %w", err)
	}
	return tool, nil
}

// MergeRequirementsIntoTool merges workflow-level and step-level requirements
// into a tool map. CWL spec priority:
//
//	tool requirements > step requirements > workflow requirements > tool hints > step hints > workflow hints
func MergeRequirementsIntoTool(tool map[string]any, wf *cwl.Workflow, stepID string) {
	if wf == nil || tool == nil {
		return
	}

	// Get or create tool requirements map.
	toolReqs, _ := tool["requirements"].(map[string]any)
	if toolReqs == nil {
		toolReqs = make(map[string]any)
	}

	toolHints, _ := tool["hints"].(map[string]any)
	if toolHints == nil {
		toolHints = make(map[string]any)
	}

	// Look up the cwl.Step for step-level requirements.
	var cwlStep *cwl.Step
	if s, ok := wf.Steps[stepID]; ok {
		cwlStep = &s
	}

	// Merge step requirements (higher priority than workflow, lower than tool).
	if cwlStep != nil && cwlStep.Requirements != nil {
		for key, val := range cwlStep.Requirements {
			if _, exists := toolReqs[key]; !exists {
				toolReqs[key] = val
			}
		}
	}

	// Merge workflow requirements (lowest priority among requirements).
	if wf.Requirements != nil {
		for key, val := range wf.Requirements {
			if _, exists := toolReqs[key]; !exists {
				toolReqs[key] = val
			}
		}
	}

	// Merge step hints.
	if cwlStep != nil && cwlStep.Hints != nil {
		for key, val := range cwlStep.Hints {
			if _, exists := toolReqs[key]; !exists {
				if _, exists := toolHints[key]; !exists {
					toolHints[key] = val
				}
			}
		}
	}

	// Merge workflow hints.
	if wf.Hints != nil {
		for key, val := range wf.Hints {
			if _, exists := toolReqs[key]; !exists {
				if _, exists := toolHints[key]; !exists {
					toolHints[key] = val
				}
			}
		}
	}

	if len(toolReqs) > 0 {
		tool["requirements"] = toolReqs
	}
	if len(toolHints) > 0 {
		tool["hints"] = toolHints
	}
}

// MergeJobRequirementsIntoTool merges cwl:requirements from the job input document
// into a tool map. Job requirements have the highest priority, overriding all other requirements.
func MergeJobRequirementsIntoTool(tool map[string]any, jobReqs []any) {
	if tool == nil || len(jobReqs) == 0 {
		return
	}

	toolReqs, _ := tool["requirements"].(map[string]any)
	if toolReqs == nil {
		toolReqs = make(map[string]any)
	}

	for _, req := range jobReqs {
		reqMap, ok := req.(map[string]any)
		if !ok {
			continue
		}
		class, _ := reqMap["class"].(string)
		if class == "" {
			continue
		}
		// Job requirements override — use class name as key.
		toolReqs[class] = reqMap
	}

	tool["requirements"] = toolReqs
}
