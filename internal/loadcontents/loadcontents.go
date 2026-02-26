// Package loadcontents provides CWL loadContents functionality with 64KB limit.
// Per CWL v1.2 spec, loadContents MUST fail if the file exceeds 64KB.
package loadcontents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxSize is the maximum file size for loadContents (64KB per CWL spec).
const MaxSize = 64 * 1024

// Process loads file contents into File objects with 64KB limit enforcement.
// Returns an error if any file exceeds the 64KB limit.
func Process(val any, cwlDir string) (any, error) {
	switch v := val.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok && class == "File" {
			return processFile(v, cwlDir)
		}
		return val, nil

	case []any:
		// Process array of Files.
		result := make([]any, len(v))
		for i, item := range v {
			processed, err := Process(item, cwlDir)
			if err != nil {
				return nil, err
			}
			result[i] = processed
		}
		return result, nil

	default:
		return val, nil
	}
}

// processFile loads contents of a single File object.
func processFile(fileObj map[string]any, cwlDir string) (map[string]any, error) {
	// Get the file path.
	path := ""
	if p, ok := fileObj["path"].(string); ok {
		path = p
	} else if loc, ok := fileObj["location"].(string); ok {
		path = strings.TrimPrefix(loc, "file://")
	}
	if path == "" {
		return nil, fmt.Errorf("File object has no path or location")
	}

	// Resolve relative paths.
	if !filepath.IsAbs(path) && cwlDir != "" {
		path = filepath.Join(cwlDir, path)
	}

	// Check file size - MUST fail if > 64KB per CWL spec.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if info.Size() > MaxSize {
		return nil, fmt.Errorf("loadContents: file %q is %d bytes, exceeds 64KB limit", path, info.Size())
	}

	// Read contents.
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file contents: %w", err)
	}

	// Create a copy of the map with contents added.
	result := make(map[string]any)
	for k, val := range fileObj {
		result[k] = val
	}
	result["contents"] = string(content)
	return result, nil
}
