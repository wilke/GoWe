package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/me/gowe/pkg/cwl"
)

// DAGResult holds the result of DAG analysis.
type DAGResult struct {
	// Edges maps each step ID to the step IDs it depends on (upstream).
	Edges map[string][]string
	// Order is the topological sort of steps (execution order).
	Order []string
}

// BuildDAG constructs a directed acyclic graph from workflow step source references.
// It uses Kahn's algorithm for topological sort and cycle detection.
//
// Source "assemble/contigs" in a step's inputs creates an edge: assemble -> this step.
// Bare sources (workflow inputs like "reads_r1") create no edges.
//
// Returns the dependency map and topological order, or an error if a cycle exists.
func BuildDAG(wf *cwl.Workflow) (*DAGResult, error) {
	stepIDs := make(map[string]bool, len(wf.Steps))
	for id := range wf.Steps {
		stepIDs[id] = true
	}

	// forward[A] = [B, C] means A must complete before B and C.
	// deps[B] = [A] means B depends on A.
	forward := make(map[string][]string, len(wf.Steps))
	deps := make(map[string][]string, len(wf.Steps))
	inDegree := make(map[string]int, len(wf.Steps))

	for id := range wf.Steps {
		inDegree[id] = 0
	}

	// Build edges from step input source references.
	for stepID, step := range wf.Steps {
		seen := make(map[string]bool)
		for _, si := range step.In {
			// Handle multiple sources.
			for _, source := range si.Sources {
				if source == "" {
					continue
				}
				if strings.Contains(source, "/") {
					depID := strings.SplitN(source, "/", 2)[0]
					if stepIDs[depID] && !seen[depID] && depID != stepID {
						seen[depID] = true
						forward[depID] = append(forward[depID], stepID)
						deps[stepID] = append(deps[stepID], depID)
						inDegree[stepID]++
					}
					// Self-loop: step depends on itself.
					if depID == stepID {
						return nil, fmt.Errorf("workflow contains a cycle involving steps: %s", stepID)
					}
				}
			}
		}
	}

	// Sort dependency lists for deterministic output.
	for id := range deps {
		sort.Strings(deps[id])
	}

	// Kahn's algorithm: BFS topological sort.
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	var order []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)

		successors := forward[node]
		sort.Strings(successors)
		for _, succ := range successors {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				queue = append(queue, succ)
			}
		}
		sort.Strings(queue)
	}

	if len(order) != len(stepIDs) {
		var cycleNodes []string
		for id, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, id)
			}
		}
		sort.Strings(cycleNodes)
		return nil, fmt.Errorf("workflow contains a cycle involving steps: %s",
			strings.Join(cycleNodes, ", "))
	}

	return &DAGResult{
		Edges: deps,
		Order: order,
	}, nil
}
