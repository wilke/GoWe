// Package fileliteral provides shared file literal handling for CWL.
// CWL file literals are File objects with "contents" field but no path/location.
// These must be materialized as actual files before execution.
package fileliteral

import (
	"os"
	"path/filepath"
)

// Materialize creates a temp file from file literal contents.
// Per CWL spec, file literals with "contents" field are written to a temp file.
// Returns the absolute path to the materialized file.
func Materialize(contents string, basename string) (string, error) {
	// Use provided basename or default.
	if basename == "" {
		basename = "cwl_literal"
	}

	// Create temp directory for file literals.
	// Resolve symlinks (e.g., /var -> /private/var on macOS) for Docker compatibility.
	tempDir := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = resolved
	}
	tempDir = filepath.Join(tempDir, "cwl-literals")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", err
	}

	// Create the temp file.
	tempPath := filepath.Join(tempDir, basename)
	if err := os.WriteFile(tempPath, []byte(contents), 0644); err != nil {
		return "", err
	}

	return tempPath, nil
}

// MaterializeFileObject materializes a file literal if the File object has
// "contents" but no "path" or "location". Returns true if the object was
// modified (file literal was materialized).
func MaterializeFileObject(obj map[string]any) (bool, error) {
	contents, hasContents := obj["contents"].(string)
	if !hasContents {
		return false, nil
	}

	_, hasPath := obj["path"]
	_, hasLocation := obj["location"]
	if hasPath || hasLocation {
		return false, nil
	}

	// File literal - materialize to temp file.
	basename := ""
	if b, ok := obj["basename"].(string); ok {
		basename = b
	}

	tempFile, err := Materialize(contents, basename)
	if err != nil {
		return false, err
	}

	obj["path"] = tempFile
	obj["location"] = "file://" + tempFile
	return true, nil
}

// MaterializeRecursive materializes file literals in a value, handling
// nested structures like Directory listings and arrays.
func MaterializeRecursive(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		// Handle File objects.
		if class == "File" {
			if _, err := MaterializeFileObject(v); err != nil {
				return nil, err
			}
		}

		// Handle Directory listings.
		if listing, ok := v["listing"].([]any); ok {
			for i, item := range listing {
				resolved, err := MaterializeRecursive(item)
				if err != nil {
					return nil, err
				}
				listing[i] = resolved
			}
		}

		// Handle secondaryFiles.
		if secFiles, ok := v["secondaryFiles"].([]any); ok {
			for i, item := range secFiles {
				resolved, err := MaterializeRecursive(item)
				if err != nil {
					return nil, err
				}
				secFiles[i] = resolved
			}
		}

		return v, nil

	case []any:
		for i, item := range v {
			resolved, err := MaterializeRecursive(item)
			if err != nil {
				return nil, err
			}
			v[i] = resolved
		}
		return v, nil

	default:
		return value, nil
	}
}
