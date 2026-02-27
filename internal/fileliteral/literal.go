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

// MaterializeInDir creates a file from contents in the specified directory.
// Unlike Materialize, this uses a custom base directory instead of the system temp dir.
func MaterializeInDir(contents string, basename string, baseDir string) (string, error) {
	if basename == "" {
		basename = "cwl_literal"
	}

	litDir := filepath.Join(baseDir, "cwl-literals")
	if err := os.MkdirAll(litDir, 0755); err != nil {
		return "", err
	}

	filePath := filepath.Join(litDir, basename)
	if err := os.WriteFile(filePath, []byte(contents), 0644); err != nil {
		return "", err
	}

	return filePath, nil
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

// RematerializeFileObject re-materializes a file from its "contents" field
// if the file has "contents" but the "path" doesn't exist on the local filesystem.
// This is needed for distributed execution where files are materialized on one
// machine but need to be re-created on another.
func RematerializeFileObject(obj map[string]any, baseDir string) (bool, error) {
	contents, hasContents := obj["contents"].(string)
	if !hasContents {
		return false, nil
	}

	// Check if path exists.
	if path, ok := obj["path"].(string); ok && path != "" {
		if _, err := os.Stat(path); err == nil {
			return false, nil // Path exists, no need to rematerialize
		}
	}

	// Path doesn't exist or not set - rematerialize from contents.
	basename := ""
	if b, ok := obj["basename"].(string); ok {
		basename = b
	}

	var newPath string
	var err error
	if baseDir != "" {
		newPath, err = MaterializeInDir(contents, basename, baseDir)
	} else {
		newPath, err = Materialize(contents, basename)
	}
	if err != nil {
		return false, err
	}

	obj["path"] = newPath
	obj["location"] = "file://" + newPath
	return true, nil
}

// RematerializeRecursive re-materializes file literals in job inputs if their
// paths don't exist locally. This handles distributed execution where file
// literals were materialized on the server but need to be re-created on the worker.
func RematerializeRecursive(value any, baseDir string) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		// Handle File objects.
		if class == "File" {
			if _, err := RematerializeFileObject(v, baseDir); err != nil {
				return nil, err
			}
		}

		// Handle Directory listings.
		if listing, ok := v["listing"].([]any); ok {
			for i, item := range listing {
				resolved, err := RematerializeRecursive(item, baseDir)
				if err != nil {
					return nil, err
				}
				listing[i] = resolved
			}
		}

		// Handle secondaryFiles.
		if secFiles, ok := v["secondaryFiles"].([]any); ok {
			for i, item := range secFiles {
				resolved, err := RematerializeRecursive(item, baseDir)
				if err != nil {
					return nil, err
				}
				secFiles[i] = resolved
			}
		}

		return v, nil

	case []any:
		for i, item := range v {
			resolved, err := RematerializeRecursive(item, baseDir)
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
