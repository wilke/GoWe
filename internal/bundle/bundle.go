package bundle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Result holds the output of bundling a workflow.
type Result struct {
	Packed []byte // The packed $graph YAML document
	Name   string // Workflow name (derived from filename)
}

// Bundle reads a workflow CWL file, resolves all run: references relative
// to its location, and produces a packed $graph document.
func Bundle(workflowPath string) (*Result, error) {
	absPath, err := filepath.Abs(workflowPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	baseDir := filepath.Dir(absPath)

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse workflow YAML: %w", err)
	}

	// If already a $graph document, return as-is
	if _, ok := doc["$graph"]; ok {
		return &Result{Packed: data, Name: nameFromPath(workflowPath)}, nil
	}

	class, _ := doc["class"].(string)
	if class != "Workflow" {
		return nil, fmt.Errorf("expected class: Workflow, got %q", class)
	}

	// Collect all tools referenced by steps
	graph := []any{}
	toolIDs := map[string]string{} // original ref → assigned ID

	steps, ok := doc["steps"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("workflow has no steps")
	}

	for stepName, stepVal := range steps {
		step, ok := stepVal.(map[string]any)
		if !ok {
			continue
		}
		runRef, ok := step["run"].(string)
		if !ok {
			continue
		}

		// Skip if already a fragment reference
		if strings.HasPrefix(runRef, "#") {
			continue
		}

		// Check if we've already loaded this tool
		if _, seen := toolIDs[runRef]; seen {
			step["run"] = "#" + toolIDs[runRef]
			continue
		}

		// Resolve and read the tool file
		toolPath := filepath.Join(baseDir, runRef)
		toolData, err := os.ReadFile(toolPath)
		if err != nil {
			return nil, fmt.Errorf("step %q: read tool %q: %w", stepName, runRef, err)
		}

		var toolDoc map[string]any
		if err := yaml.Unmarshal(toolData, &toolDoc); err != nil {
			return nil, fmt.Errorf("step %q: parse tool %q: %w", stepName, runRef, err)
		}

		// Assign an ID to the tool (use filename without extension)
		toolID := strings.TrimSuffix(filepath.Base(runRef), filepath.Ext(runRef))
		toolDoc["id"] = toolID
		toolIDs[runRef] = toolID

		// Remove cwlVersion from individual tools (it's at the top level)
		delete(toolDoc, "cwlVersion")

		graph = append(graph, toolDoc)

		// Replace run: with fragment reference
		step["run"] = "#" + toolID
	}

	// Add the workflow itself to the graph
	wfDoc := make(map[string]any)
	for k, v := range doc {
		if k == "cwlVersion" {
			continue
		}
		wfDoc[k] = v
	}
	wfDoc["id"] = "main"
	graph = append(graph, wfDoc)

	// Build the packed document
	packed := map[string]any{
		"cwlVersion": doc["cwlVersion"],
		"$graph":     graph,
	}

	out, err := yaml.Marshal(packed)
	if err != nil {
		return nil, fmt.Errorf("marshal packed document: %w", err)
	}

	return &Result{
		Packed: out,
		Name:   nameFromPath(workflowPath),
	}, nil
}

// nameFromPath derives a workflow name from its file path.
func nameFromPath(path string) string {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return name
}
