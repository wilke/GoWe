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
	Packed    []byte // The packed $graph YAML document
	Name      string // Workflow name (derived from filename)
	ProcessID string // Process ID from #fragment (e.g., "main" from "file.cwl#main")
}

// Bundle reads a workflow CWL file, resolves all run: references relative
// to its location, and produces a packed $graph document.
// If workflowPath contains a #fragment (e.g., "file.cwl#main"), the fragment
// is extracted and returned in Result.ProcessID for process selection.
func Bundle(workflowPath string) (*Result, error) {
	// Parse #fragment from path (like cwl-runner does).
	var processID string
	if idx := strings.Index(workflowPath, "#"); idx != -1 {
		processID = workflowPath[idx+1:]
		workflowPath = workflowPath[:idx]
	}

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

	// Resolve $import directives before further processing.
	resolved, err := resolveImports(doc, baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve imports: %w", err)
	}
	doc = resolved.(map[string]any)

	// If already a $graph document, re-marshal with resolved imports and return.
	if _, ok := doc["$graph"]; ok {
		packed, err := yaml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal packed document: %w", err)
		}
		return &Result{Packed: packed, Name: nameFromPath(workflowPath), ProcessID: processID}, nil
	}

	class, _ := doc["class"].(string)
	if class == "CommandLineTool" || class == "ExpressionTool" {
		// Wrap bare tool in a synthetic single-step workflow
		return bundleBareTool(doc, workflowPath, processID)
	}
	if class != "Workflow" {
		return nil, fmt.Errorf("expected class: Workflow, CommandLineTool, or ExpressionTool, got %q", class)
	}

	// Collect all tools referenced by steps
	graph := []any{}
	toolIDs := map[string]string{} // original ref → assigned ID

	// Steps can be a map or an array (including empty array for pass-through workflows).
	var steps map[string]any
	switch s := doc["steps"].(type) {
	case map[string]any:
		steps = s
	case []any:
		// Convert array-style steps to map, or handle empty array.
		steps = make(map[string]any)
		for _, item := range s {
			if stepMap, ok := item.(map[string]any); ok {
				if id, ok := stepMap["id"].(string); ok {
					// Strip any prefix like "#main/"
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

	for stepName, stepVal := range steps {
		step, ok := stepVal.(map[string]any)
		if !ok {
			continue
		}

		// Resolve file paths in step input defaults.
		if stepIn, ok := step["in"].(map[string]any); ok {
			for _, inputVal := range stepIn {
				if inputMap, ok := inputVal.(map[string]any); ok {
					if def, ok := inputMap["default"]; ok {
						inputMap["default"] = ResolveFilePaths(def, baseDir)
					}
				}
			}
		} else if stepInArr, ok := step["in"].([]any); ok {
			for _, inputItem := range stepInArr {
				if inputMap, ok := inputItem.(map[string]any); ok {
					if def, ok := inputMap["default"]; ok {
						inputMap["default"] = ResolveFilePaths(def, baseDir)
					}
				}
			}
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
		Packed:    out,
		Name:      nameFromPath(workflowPath),
		ProcessID: processID,
	}, nil
}

// nameFromPath derives a workflow name from its file path.
func nameFromPath(path string) string {
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return name
}

// bundleBareTool wraps a bare CommandLineTool or ExpressionTool in a synthetic
// single-step workflow, producing a packed $graph document.
func bundleBareTool(toolDoc map[string]any, toolPath string, processID string) (*Result, error) {
	toolID := "tool"
	cwlVersion := toolDoc["cwlVersion"]

	// Get the base directory for resolving relative paths.
	absPath, err := filepath.Abs(toolPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	baseDir := filepath.Dir(absPath)

	// Parse tool inputs
	inputs := normalizeToMap(toolDoc["inputs"])
	outputs := normalizeToMap(toolDoc["outputs"])

	// Build workflow inputs (same as tool inputs)
	wfInputs := make(map[string]any)
	stepIn := make(map[string]any)
	for id, inp := range inputs {
		// Extract type from input definition
		var inputType any
		switch v := inp.(type) {
		case string:
			inputType = v
		case map[string]any:
			inputType = v["type"]
			// Copy default if present, resolving File/Directory locations.
			if def, ok := v["default"]; ok {
				resolvedDef := ResolveFilePaths(def, baseDir)
				wfInputs[id] = map[string]any{"type": inputType, "default": resolvedDef}
				stepIn[id] = id
				continue
			}
		}
		wfInputs[id] = map[string]any{"type": inputType}
		stepIn[id] = id
	}

	// Build workflow outputs (same as tool outputs, with outputSource)
	wfOutputs := make(map[string]any)
	stepOut := []string{}
	for id, out := range outputs {
		var outputType any
		switch v := out.(type) {
		case string:
			outputType = v
		case map[string]any:
			outputType = v["type"]
		}
		wfOutputs[id] = map[string]any{
			"type":         outputType,
			"outputSource": "run_tool/" + id,
		}
		stepOut = append(stepOut, id)
	}

	// Create synthetic workflow
	workflow := map[string]any{
		"id":      "main",
		"class":   "Workflow",
		"inputs":  wfInputs,
		"outputs": wfOutputs,
		"steps": map[string]any{
			"run_tool": map[string]any{
				"run": "#" + toolID,
				"in":  stepIn,
				"out": stepOut,
			},
		},
	}

	// Prepare tool for graph (remove cwlVersion, add id, resolve paths).
	toolForGraph := make(map[string]any)
	for k, v := range toolDoc {
		if k == "cwlVersion" {
			continue
		}
		// Resolve file paths in inputs (for default Files/Directories).
		if k == "inputs" {
			toolForGraph[k] = ResolveFilePaths(v, baseDir)
		} else {
			toolForGraph[k] = v
		}
	}
	toolForGraph["id"] = toolID

	// Build packed document
	packed := map[string]any{
		"cwlVersion": cwlVersion,
		"$graph":     []any{toolForGraph, workflow},
	}

	out, err := yaml.Marshal(packed)
	if err != nil {
		return nil, fmt.Errorf("marshal packed document: %w", err)
	}

	return &Result{
		Packed:    out,
		Name:      nameFromPath(toolPath),
		ProcessID: processID,
	}, nil
}

// ResolveFilePaths resolves relative File/Directory locations to absolute paths.
// For File/Directory objects, it also ensures that the 'path' property is set
// from 'location' if not already present, which is required for CWL expressions
// like $(inputs.file1.path) to work correctly.
func ResolveFilePaths(v any, baseDir string) any {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			// Make a copy and resolve the location.
			result := make(map[string]any)
			for k, v := range val {
				result[k] = v
			}

			// Resolve location to absolute path.
			var absLocation string
			if loc, ok := result["location"].(string); ok {
				if strings.HasPrefix(loc, "file://") {
					// Strip file:// prefix and resolve.
					localPath := strings.TrimPrefix(loc, "file://")
					if !filepath.IsAbs(localPath) {
						localPath = filepath.Join(baseDir, localPath)
					}
					absLocation = localPath
					result["location"] = "file://" + localPath
				} else if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
					// Relative local path.
					if !filepath.IsAbs(loc) {
						absLocation = filepath.Join(baseDir, loc)
					} else {
						absLocation = loc
					}
					result["location"] = absLocation
				}
			}

			// Resolve path to absolute, or set from location if not present.
			var resolvedPath string
			if path, ok := result["path"].(string); ok {
				if !filepath.IsAbs(path) && !strings.HasPrefix(path, "file://") {
					resolvedPath = filepath.Join(baseDir, path)
				} else {
					resolvedPath = path
				}
				result["path"] = resolvedPath
			} else if absLocation != "" {
				// Set path from resolved location (required for CWL expressions).
				resolvedPath = absLocation
				result["path"] = absLocation
			}

			// For File objects, compute basename, nameroot, and nameext from path.
			if class == "File" && resolvedPath != "" {
				basename := filepath.Base(resolvedPath)
				if _, ok := result["basename"]; !ok {
					result["basename"] = basename
				}
				ext := filepath.Ext(basename)
				if _, ok := result["nameext"]; !ok {
					result["nameext"] = ext
				}
				if _, ok := result["nameroot"]; !ok {
					result["nameroot"] = strings.TrimSuffix(basename, ext)
				}
			}

			// For Directory objects, compute basename from path.
			if class == "Directory" && resolvedPath != "" {
				if _, ok := result["basename"]; !ok {
					result["basename"] = filepath.Base(resolvedPath)
				}
			}

			// Recursively resolve secondary files.
			if sf, ok := result["secondaryFiles"].([]any); ok {
				resolved := make([]any, len(sf))
				for i, item := range sf {
					resolved[i] = ResolveFilePaths(item, baseDir)
				}
				result["secondaryFiles"] = resolved
			}

			// Recursively resolve listing entries for directories.
			if listing, ok := result["listing"].([]any); ok {
				resolved := make([]any, len(listing))
				for i, item := range listing {
					resolved[i] = ResolveFilePaths(item, baseDir)
				}
				result["listing"] = resolved
			}
			return result
		}
		// For other maps, recursively process.
		result := make(map[string]any)
		for k, v := range val {
			result[k] = ResolveFilePaths(v, baseDir)
		}
		return result
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = ResolveFilePaths(item, baseDir)
		}
		return result
	default:
		return v
	}
}

// normalizeToMap converts array-style CWL definitions to map-style.
func normalizeToMap(v any) map[string]any {
	switch val := v.(type) {
	case map[string]any:
		return val
	case []any:
		result := make(map[string]any)
		for _, item := range val {
			if m, ok := item.(map[string]any); ok {
				if id, ok := m["id"].(string); ok {
					// Strip packed format prefix if present
					if idx := strings.LastIndex(id, "/"); idx >= 0 {
						id = id[idx+1:]
					}
					id = strings.TrimPrefix(id, "#")
					result[id] = m
				}
			}
		}
		return result
	}
	return make(map[string]any)
}

// resolveImports recursively resolves $import directives in a CWL document.
// It loads referenced files and replaces the $import directive with the file contents.
func resolveImports(v any, baseDir string) (any, error) {
	switch val := v.(type) {
	case map[string]any:
		// Check if this is an $import directive.
		if importPath, ok := val["$import"].(string); ok && len(val) == 1 {
			// Resolve the import path relative to baseDir.
			fullPath := importPath
			if !filepath.IsAbs(importPath) {
				fullPath = filepath.Join(baseDir, importPath)
			}

			// Read and parse the imported file.
			data, err := os.ReadFile(fullPath)
			if err != nil {
				return nil, fmt.Errorf("read import %q: %w", importPath, err)
			}

			var imported any
			if err := yaml.Unmarshal(data, &imported); err != nil {
				return nil, fmt.Errorf("parse import %q: %w", importPath, err)
			}

			// Recursively resolve imports in the imported content.
			importDir := filepath.Dir(fullPath)
			return resolveImports(imported, importDir)
		}

		// Recursively process all values in the map.
		result := make(map[string]any)
		for k, v := range val {
			resolved, err := resolveImports(v, baseDir)
			if err != nil {
				return nil, err
			}
			result[k] = resolved
		}
		return result, nil

	case []any:
		// Recursively process all elements in the array.
		result := make([]any, len(val))
		for i, item := range val {
			resolved, err := resolveImports(item, baseDir)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil

	default:
		// Primitive values are returned as-is.
		return v, nil
	}
}
