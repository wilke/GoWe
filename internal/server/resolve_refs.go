package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
	"gopkg.in/yaml.v3"
)

const goweURIScheme = "gowe://"

// resolveGoweRefs scans a CWL document for run: gowe://... references,
// looks up each referenced workflow/tool in the store, and inlines them
// into a packed $graph document. This allows workflows to compose
// already-registered tools by reference.
//
// If the document contains no gowe:// references, it is returned unchanged.
//
// Reference formats:
//   - gowe://workflow-name     (looked up by name, most recent version)
//   - gowe://wf_<uuid>        (looked up by exact ID)
func resolveGoweRefs(ctx context.Context, st store.Store, rawCWL string) (string, error) {
	// Quick check: skip if no gowe:// references.
	if !strings.Contains(rawCWL, goweURIScheme) {
		return rawCWL, nil
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(rawCWL), &doc); err != nil {
		return "", fmt.Errorf("parse CWL for reference resolution: %w", err)
	}

	// Pre-packed $graph documents are not supported for gowe:// reference
	// resolution; callers must provide a bare Workflow document instead.
	if _, hasGraph := doc["$graph"]; hasGraph {
		return "", fmt.Errorf("gowe:// references in pre-packed $graph documents are not supported; use a bare Workflow document")
	}

	class, _ := doc["class"].(string)
	if class != "Workflow" {
		return rawCWL, nil // Only workflows can have run: references
	}

	steps := extractStepsMap(doc)
	if steps == nil {
		return rawCWL, nil
	}

	// Collect all gowe:// references.
	refs := map[string]bool{} // ref → true
	for _, stepVal := range steps {
		step, ok := stepVal.(map[string]any)
		if !ok {
			continue
		}
		runRef, ok := step["run"].(string)
		if !ok || !strings.HasPrefix(runRef, goweURIScheme) {
			continue
		}
		refs[runRef] = true
	}

	if len(refs) == 0 {
		return rawCWL, nil
	}

	// Sort refs for deterministic graph entry order and ID assignment.
	sortedRefs := make([]string, 0, len(refs))
	for ref := range refs {
		sortedRefs = append(sortedRefs, ref)
	}
	sort.Strings(sortedRefs)

	// Resolve each reference and collect tool graphs.
	type resolvedTool struct {
		id         string // assigned graph ID for this tool
		graphItems []any  // items to add to the $graph
		namespaces map[string]string
	}
	resolved := map[string]*resolvedTool{}
	seen := map[string]bool{} // track IDs to avoid collisions

	for _, ref := range sortedRefs {
		name := strings.TrimPrefix(ref, goweURIScheme)
		if name == "" {
			return "", fmt.Errorf("empty gowe:// reference")
		}

		wf, err := lookupWorkflow(ctx, st, name)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", ref, err)
		}
		if wf == nil {
			return "", fmt.Errorf("resolve %s: workflow not found", ref)
		}

		// Parse the referenced tool's RawCWL to extract its graph items.
		var refDoc map[string]any
		if err := yaml.Unmarshal([]byte(wf.RawCWL), &refDoc); err != nil {
			return "", fmt.Errorf("parse referenced workflow %s: %w", ref, err)
		}

		rt := &resolvedTool{}

		// Extract namespaces from the referenced document.
		if ns, ok := refDoc["$namespaces"].(map[string]any); ok {
			rt.namespaces = make(map[string]string)
			for k, v := range ns {
				if s, ok := v.(string); ok {
					rt.namespaces[k] = s
				}
			}
		}

		if graphItems, ok := refDoc["$graph"].([]any); ok {
			// Packed document: find the main tool/workflow.
			// For a bare tool wrapped by the bundler, the graph has [tool, synthetic-workflow].
			// We need the actual tool, not the synthetic wrapper.
			toolItem, _ := findMainTool(graphItems, wf.Name)
			if toolItem == nil {
				return "", fmt.Errorf("resolve %s: no tool found in packed document", ref)
			}

			// Use the workflow name as the graph ID, deconflicting if needed.
			// toolID is often a synthetic bundler ID like "tool"; prefer wf.Name.
			id := uniqueID(wf.Name, seen)
			setMapField(toolItem, "id", id)
			delete(toolItem, "cwlVersion")
			seen[id] = true

			rt.id = id
			rt.graphItems = []any{toolItem}
		} else {
			// Bare document (single tool/workflow).
			class, _ := refDoc["class"].(string)
			if class != "CommandLineTool" && class != "ExpressionTool" {
				return "", fmt.Errorf("resolve %s: expected CommandLineTool or ExpressionTool, got %q", ref, class)
			}

			id := uniqueID(wf.Name, seen)
			delete(refDoc, "cwlVersion")
			delete(refDoc, "$namespaces")
			refDoc["id"] = id
			seen[id] = true

			rt.id = id
			rt.graphItems = []any{refDoc}
		}

		resolved[ref] = rt
	}

	// Build the packed $graph document.
	graph := []any{}

	// Collect namespaces from all sources.
	allNamespaces := make(map[string]string)
	if ns, ok := doc["$namespaces"].(map[string]any); ok {
		for k, v := range ns {
			if s, ok := v.(string); ok {
				allNamespaces[k] = s
			}
		}
	}

	// Add resolved tool items to graph in stable order and merge namespaces.
	for _, ref := range sortedRefs {
		rt := resolved[ref]
		graph = append(graph, rt.graphItems...)
		for k, v := range rt.namespaces {
			allNamespaces[k] = v
		}
	}

	// Update step run: references to fragment references.
	for _, stepVal := range steps {
		step, ok := stepVal.(map[string]any)
		if !ok {
			continue
		}
		runRef, ok := step["run"].(string)
		if !ok || !strings.HasPrefix(runRef, goweURIScheme) {
			continue
		}
		rt := resolved[runRef]
		step["run"] = "#" + rt.id
	}

	// Add the workflow itself to the graph.
	wfDoc := make(map[string]any)
	for k, v := range doc {
		if k == "cwlVersion" || k == "$namespaces" {
			continue
		}
		wfDoc[k] = v
	}
	wfDoc["id"] = "main"
	graph = append(graph, wfDoc)

	// Build final packed document.
	packed := map[string]any{
		"cwlVersion": doc["cwlVersion"],
		"$graph":     graph,
	}
	if len(allNamespaces) > 0 {
		packed["$namespaces"] = allNamespaces
	}

	out, err := yaml.Marshal(packed)
	if err != nil {
		return "", fmt.Errorf("marshal resolved document: %w", err)
	}
	return string(out), nil
}

// lookupWorkflow resolves a name-or-ID to a workflow.
// IDs starting with "wf_" are looked up by exact ID; otherwise by name.
func lookupWorkflow(ctx context.Context, st store.Store, nameOrID string) (*model.Workflow, error) {
	if strings.HasPrefix(nameOrID, "wf_") {
		return st.GetWorkflow(ctx, nameOrID)
	}
	return st.GetWorkflowByName(ctx, nameOrID)
}

// findMainTool finds the primary tool in a $graph document.
// For bundled bare tools, the graph is [tool, synthetic-workflow] — we want the tool.
// For bundled workflows, we look for a non-Workflow entry.
func findMainTool(graph []any, fallbackID string) (map[string]any, string) {
	for _, item := range graph {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		class, _ := m["class"].(string)
		if class == "CommandLineTool" || class == "ExpressionTool" {
			id, _ := m["id"].(string)
			if id == "" {
				id = fallbackID
			}
			return m, id
		}
	}
	return nil, ""
}

// uniqueID returns id if not already in seen, otherwise appends a suffix.
func uniqueID(id string, seen map[string]bool) string {
	if !seen[id] {
		return id
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s_%d", id, i)
		if !seen[candidate] {
			return candidate
		}
	}
}

// extractStepsMap extracts the steps map from a workflow document.
func extractStepsMap(doc map[string]any) map[string]any {
	switch s := doc["steps"].(type) {
	case map[string]any:
		return s
	case []any:
		steps := make(map[string]any)
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
		// Write back so mutations are reflected.
		doc["steps"] = steps
		return steps
	}
	return nil
}

// setMapField sets a field on a map[string]any, used for setting "id".
func setMapField(m map[string]any, key string, value any) {
	m[key] = value
}
