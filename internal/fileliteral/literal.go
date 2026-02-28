// Package fileliteral provides shared file literal handling for CWL.
// CWL file literals are File objects with "contents" field but no path/location.
// These must be materialized as actual files before execution.
package fileliteral

import (
	"crypto/sha1"
	"fmt"
	"io"
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

// MaterializeOutputs processes ExpressionTool outputs to materialize file/directory
// literals and add required metadata (checksum, size, location).
// outDir is where materialized files should be written.
func MaterializeOutputs(outputs map[string]any, outDir string) (map[string]any, error) {
	result := make(map[string]any)
	for k, v := range outputs {
		materialized, err := materializeOutputValue(v, outDir)
		if err != nil {
			return nil, fmt.Errorf("output %s: %w", k, err)
		}
		result[k] = materialized
	}
	return result, nil
}

// materializeOutputValue recursively materializes output values.
func materializeOutputValue(value any, outDir string) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		if class == "File" {
			return materializeFileOutput(v, outDir)
		}
		if class == "Directory" {
			return materializeDirOutput(v, outDir)
		}

		// Recursively process other map values.
		result := make(map[string]any)
		for key, val := range v {
			processed, err := materializeOutputValue(val, outDir)
			if err != nil {
				return nil, err
			}
			result[key] = processed
		}
		return result, nil

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			processed, err := materializeOutputValue(item, outDir)
			if err != nil {
				return nil, err
			}
			result[i] = processed
		}
		return result, nil

	default:
		return value, nil
	}
}

// materializeFileOutput materializes a File output.
func materializeFileOutput(obj map[string]any, outDir string) (map[string]any, error) {
	result := make(map[string]any)
	for k, v := range obj {
		result[k] = v
	}
	result["class"] = "File"

	// If file has contents but no path, write it to disk.
	contents, hasContents := result["contents"].(string)
	_, hasPath := result["path"].(string)
	_, hasLocation := result["location"].(string)

	if hasContents && !hasPath && !hasLocation {
		basename := "cwl_file"
		if b, ok := result["basename"].(string); ok && b != "" {
			basename = b
		}

		filePath := filepath.Join(outDir, basename)
		if err := os.WriteFile(filePath, []byte(contents), 0644); err != nil {
			return nil, fmt.Errorf("write file literal: %w", err)
		}

		result["path"] = filePath
		result["location"] = basename
		result["size"] = int64(len(contents))
		result["checksum"] = computeSHA1String(contents)

		// Remove contents from output (it's now on disk).
		delete(result, "contents")
	} else if hasPath {
		// File exists on disk - add metadata if missing.
		path := result["path"].(string)
		if err := addFileMetadata(result, path); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// materializeDirOutput materializes a Directory output.
func materializeDirOutput(obj map[string]any, outDir string) (map[string]any, error) {
	result := make(map[string]any)
	for k, v := range obj {
		result[k] = v
	}
	result["class"] = "Directory"

	// Get directory name.
	basename := "cwl_dir"
	if b, ok := result["basename"].(string); ok && b != "" {
		basename = b
	}

	_, hasPath := result["path"].(string)
	_, hasLocation := result["location"].(string)

	// If directory has no path but has listing, create it.
	listing, hasListing := result["listing"].([]any)
	if !hasPath && !hasLocation && hasListing {
		dirPath := filepath.Join(outDir, basename)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return nil, fmt.Errorf("create directory: %w", err)
		}

		// Process listing items and symlink them into the directory.
		newListing := make([]any, 0, len(listing))
		for _, item := range listing {
			fileObj, ok := item.(map[string]any)
			if !ok {
				continue
			}

			// Materialize each item.
			processed, err := materializeOutputValue(item, dirPath)
			if err != nil {
				return nil, err
			}
			processedObj, ok := processed.(map[string]any)
			if !ok {
				continue
			}

			class, _ := processedObj["class"].(string)
			if class == "File" {
				// If file has a source path, symlink it into directory.
				srcPath := ""
				if p, ok := fileObj["path"].(string); ok {
					srcPath = p
				}

				itemBasename := "file"
				if b, ok := processedObj["basename"].(string); ok {
					itemBasename = b
				}

				if srcPath != "" {
					// Create symlink.
					linkPath := filepath.Join(dirPath, itemBasename)
					_ = os.Remove(linkPath)
					if err := os.Symlink(srcPath, linkPath); err != nil {
						// Fallback to copy.
						data, err := os.ReadFile(srcPath)
						if err != nil {
							return nil, fmt.Errorf("read source file: %w", err)
						}
						if err := os.WriteFile(linkPath, data, 0644); err != nil {
							return nil, fmt.Errorf("copy file: %w", err)
						}
					}

					// Update processed object with new location.
					processedObj["location"] = itemBasename
					if err := addFileMetadata(processedObj, srcPath); err != nil {
						return nil, err
					}
				}
			}

			newListing = append(newListing, processedObj)
		}

		result["listing"] = newListing
		result["path"] = dirPath
		result["location"] = basename
	}

	return result, nil
}

// addFileMetadata adds checksum and size to a file object if missing.
func addFileMetadata(obj map[string]any, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // File doesn't exist, skip metadata
	}

	if _, hasSize := obj["size"]; !hasSize {
		obj["size"] = info.Size()
	}

	if _, hasChecksum := obj["checksum"]; !hasChecksum {
		checksum, err := computeSHA1File(path)
		if err == nil {
			obj["checksum"] = checksum
		}
	}

	return nil
}

// computeSHA1String computes SHA1 checksum of a string.
func computeSHA1String(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	return fmt.Sprintf("sha1$%x", h.Sum(nil))
}

// computeSHA1File computes SHA1 checksum of a file.
func computeSHA1File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("sha1$%x", h.Sum(nil)), nil
}
