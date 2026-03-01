package iwdr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/fileliteral"
)

// resolveIWDListing resolves the listing field which can be an array or an expression.
func resolveIWDListing(listingRaw any, inputs map[string]any, evaluator *cwlexpr.Evaluator) ([]any, error) {
	switch v := listingRaw.(type) {
	case []any:
		return v, nil
	case string:
		// listing itself is an expression.
		if cwlexpr.IsExpression(v) {
			ctx := cwlexpr.NewContext(inputs)
			result, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return nil, fmt.Errorf("evaluate listing expression: %w", err)
			}
			if arr, ok := result.([]any); ok {
				return arr, nil
			}
			// If result is a string that looks like JSON array (from YAML | blocks
			// which append trailing newline causing stringification), try to parse it.
			if str, ok := result.(string); ok {
				str = strings.TrimSpace(str)
				if strings.HasPrefix(str, "[") {
					var arr []any
					if err := json.Unmarshal([]byte(str), &arr); err == nil {
						return arr, nil
					}
				}
			}
			if result == nil {
				return nil, nil
			}
			// Single item.
			return []any{result}, nil
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// stageIWDItem stages a single item from the InitialWorkDirRequirement listing.
// An item can be: a Dirent (map with entry/entryname), a File/Directory object,
// a string expression that evaluates to File/Directory/array, or null.
// Returns ContainerMounts for items with absolute entryname paths.
// allowAbsoluteEntryname controls whether absolute paths in entryname are permitted.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDItem(item any, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, cwlDir string, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	switch v := item.(type) {
	case map[string]any:
		// Check if this is a File or Directory object (has "class" field).
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				resolveIWDObjectPaths(v, cwlDir)
				return stageIWDFile(v, "", false, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			case "Directory":
				resolveIWDObjectPaths(v, cwlDir)
				return stageIWDDirectory(v, "", false, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			}
		}
		// Otherwise treat as Dirent.
		return stageIWDDirent(v, inputs, workDir, evaluator, cwlDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)

	case []any:
		// An array in listing — flatten and stage each item.
		var mounts []ContainerMount
		for _, sub := range v {
			if sub == nil {
				continue
			}
			subMounts, err := stageIWDItem(sub, inputs, workDir, evaluator, cwlDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			if err != nil {
				return nil, err
			}
			mounts = append(mounts, subMounts...)
		}
		return mounts, nil

	case string:
		// A bare expression in listing (e.g., "$(inputs.input_file)").
		if cwlexpr.IsExpression(v) {
			ctx := cwlexpr.NewContext(inputs)
			result, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return nil, fmt.Errorf("evaluate listing item: %w", err)
			}
			return stageIWDEvaluatedResult(result, "", false, workDir, inputs, evaluator, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
		}
		return nil, nil

	default:
		return nil, nil
	}
}

// resolveIWDObjectPaths resolves relative location/path fields in File/Directory
// objects found directly in InitialWorkDirRequirement listing, relative to the CWL file directory.
func resolveIWDObjectPaths(obj map[string]any, cwlDir string) {
	if cwlDir == "" {
		return
	}
	pathResolved := false
	if loc, ok := obj["location"].(string); ok && loc != "" && !filepath.IsAbs(loc) && !isURI(loc) {
		absLoc := filepath.Clean(filepath.Join(cwlDir, loc))
		obj["location"] = absLoc
		if _, hasPath := obj["path"]; !hasPath {
			obj["path"] = absLoc
			pathResolved = true
		}
	}
	if !pathResolved {
		if p, ok := obj["path"].(string); ok && p != "" && !filepath.IsAbs(p) {
			obj["path"] = filepath.Clean(filepath.Join(cwlDir, p))
		}
	}
	// Resolve listing entries recursively.
	if listing, ok := obj["listing"].([]any); ok {
		for _, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				resolveIWDObjectPaths(itemMap, cwlDir)
			}
		}
	}
}

// stageIWDDirent stages a Dirent entry (map with entry, optional entryname and writable).
// Returns ContainerMounts for items with absolute entryname paths.
// allowAbsoluteEntryname controls whether absolute paths in entryname are permitted.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDDirent(dirent map[string]any, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, cwlDir string, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	entryname, _ := dirent["entryname"].(string)
	entryRaw := dirent["entry"]
	writable, _ := dirent["writable"].(bool)

	// entry can be nil (e.g., $(null)).
	if entryRaw == nil {
		return nil, nil
	}

	// Validate entryname: must not contain path traversal (../).
	// Absolute paths (starting with /) are only allowed when DockerRequirement is a requirement.
	if entryname != "" && !strings.HasPrefix(entryname, "/") {
		cleaned := filepath.Clean(entryname)
		if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
			return nil, fmt.Errorf("entryname %q is invalid: must not reference parent directory", entryname)
		}
	}

	// Evaluate entryname if it's an expression.
	if entryname != "" && cwlexpr.IsExpression(entryname) {
		ctx := cwlexpr.NewContext(inputs)
		evaluated, err := evaluator.Evaluate(entryname, ctx)
		if err != nil {
			return nil, fmt.Errorf("evaluate entryname %q: %w", entryname, err)
		}
		entryname = fmt.Sprintf("%v", evaluated)
	}

	// Validate absolute entryname: only allowed when DockerRequirement is in requirements.
	if strings.HasPrefix(entryname, "/") && !allowAbsoluteEntryname {
		return nil, fmt.Errorf("absolute entryname %q requires DockerRequirement in requirements (not hints)", entryname)
	}

	// Evaluate entry.
	switch v := entryRaw.(type) {
	case string:
		return stageIWDStringEntry(v, entryname, writable, inputs, workDir, evaluator, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
	case map[string]any:
		// File/Directory object literal.
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				return stageIWDFile(v, entryname, writable, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			case "Directory":
				return stageIWDDirectory(v, entryname, writable, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			}
		}
		return nil, nil
	default:
		return nil, nil
	}
}

// stageIWDStringEntry handles a Dirent entry that is a string (literal or expression).
// Returns ContainerMounts for items with absolute entryname paths.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDStringEntry(entry, entryname string, writable bool, inputs map[string]any, workDir string, evaluator *cwlexpr.Evaluator, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	if !cwlexpr.IsExpression(entry) {
		// Pure literal string content — unescape \$( to $(.
		content := strings.ReplaceAll(entry, "\\$(", "$(")
		content = strings.ReplaceAll(content, "\\${", "${")
		if entryname == "" {
			return nil, nil
		}
		return writeIWDFileWithMounts(workDir, entryname, content, copyForContainer, allowAbsoluteEntryname)
	}

	// Check if the entire string is a single expression (no surrounding text).
	// If so, the result type determines behavior (File object vs string content).
	// Important: check on the original entry, not trimmed — YAML | adds trailing \n
	// which makes it NOT a sole expression (content should include the trailing text).
	isSoleExpr := cwlexpr.IsSoleExpression(entry)

	ctx := cwlexpr.NewContext(inputs)
	evaluated, err := evaluator.Evaluate(entry, ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluate entry for %q: %w", entryname, err)
	}

	if isSoleExpr {
		// Single expression — result could be File, Directory, array, string, number, etc.
		return stageIWDEvaluatedResult(evaluated, entryname, writable, workDir, inputs, evaluator, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
	}

	// String interpolation — result is usually string content.
	// However, if the expression returns an array or object (e.g., YAML pipe block with
	// trailing newline makes IsSoleExpression return false), we should JSON serialize it.
	// Per CWL spec test iwd-jsondump*-nl, JSON output should include trailing newline.
	var content string
	switch v := evaluated.(type) {
	case []any:
		// Array result — serialize to JSON with trailing newline.
		content = iwdResultToString(v) + "\n"
	case map[string]any:
		// Object result — check if it's a File/Directory.
		if class, ok := v["class"].(string); ok && (class == "File" || class == "Directory") {
			return stageIWDEvaluatedResult(evaluated, entryname, writable, workDir, inputs, evaluator, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
		}
		content = iwdResultToString(v) + "\n"
	case string:
		// String result — use as-is (includes any trailing text from original expression).
		content = v
	default:
		// Number, boolean, etc. — serialize with trailing newline (for YAML pipe blocks).
		content = iwdResultToString(v) + "\n"
	}
	if entryname == "" {
		return nil, nil
	}
	return writeIWDFileWithMounts(workDir, entryname, content, copyForContainer, allowAbsoluteEntryname)
}

// stageIWDEvaluatedResult stages the result of evaluating an expression.
// The result can be a File, Directory, array, string, number, null, etc.
// Returns ContainerMounts for items with absolute entryname paths.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDEvaluatedResult(result any, entryname string, writable bool, workDir string, inputs map[string]any, evaluator *cwlexpr.Evaluator, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	if result == nil {
		return nil, nil
	}

	switch v := result.(type) {
	case map[string]any:
		if class, ok := v["class"].(string); ok {
			switch class {
			case "File":
				return stageIWDFile(v, entryname, writable, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			case "Directory":
				return stageIWDDirectory(v, entryname, writable, workDir, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
			}
		}
		// Object that isn't File/Directory — serialize to JSON.
		if entryname != "" {
			return writeIWDFileWithMounts(workDir, entryname, iwdResultToString(v), copyForContainer, allowAbsoluteEntryname)
		}
		return nil, nil

	case []any:
		// Could be an array of File/Directory objects or a JSON array to serialize.
		if len(v) > 0 {
			if first, ok := v[0].(map[string]any); ok {
				if class, ok := first["class"].(string); ok && (class == "File" || class == "Directory") {
					// Array of File/Directory objects — stage each.
					var mounts []ContainerMount
					for _, item := range v {
						itemMounts, err := stageIWDEvaluatedResult(item, "", writable, workDir, inputs, evaluator, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
						if err != nil {
							return nil, err
						}
						mounts = append(mounts, itemMounts...)
					}
					return mounts, nil
				}
			}
		}
		// JSON array — serialize.
		if entryname != "" {
			return writeIWDFileWithMounts(workDir, entryname, iwdResultToString(v), copyForContainer, allowAbsoluteEntryname)
		}
		return nil, nil

	case string:
		if entryname != "" {
			return writeIWDFileWithMounts(workDir, entryname, v, copyForContainer, allowAbsoluteEntryname)
		}
		return nil, nil

	default:
		// Number, bool, etc.
		if entryname != "" {
			return writeIWDFileWithMounts(workDir, entryname, iwdResultToString(v), copyForContainer, allowAbsoluteEntryname)
		}
		return nil, nil
	}
}

// stageIWDFile stages a CWL File object into the work directory.
// When copyForContainer is true, files are copied instead of symlinked (for Docker/Apptainer).
// If entryname is an absolute path and copyForContainer is true, returns a ContainerMount
// for the file to be mounted at that path inside the container.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDFile(fileObj map[string]any, entryname string, writable bool, workDir string, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	// Handle file literals: File objects with "contents" but no path/location.
	// These need to be materialized as actual files before staging.
	if _, err := fileliteral.MaterializeFileObject(fileObj); err != nil {
		return nil, fmt.Errorf("materialize file literal: %w", err)
	}

	// Get source path.
	srcPath := ""
	if p, ok := fileObj["path"].(string); ok {
		srcPath = p
	} else if loc, ok := fileObj["location"].(string); ok {
		srcPath = strings.TrimPrefix(loc, "file://")
	}

	if srcPath == "" {
		return nil, nil
	}

	// Handle absolute entryname for container execution.
	// Per CWL spec: "When executing in a container, entryname may be an absolute path."
	if copyForContainer && entryname != "" && strings.HasPrefix(entryname, "/") {
		// Get absolute source path.
		absSrc, err := filepath.Abs(srcPath)
		if err != nil {
			absSrc = srcPath
		}
		// Return a container mount for this file.
		return []ContainerMount{{
			HostPath:      absSrc,
			ContainerPath: entryname,
			IsDirectory:   false,
		}}, nil
	}

	// Determine destination name (for non-absolute entrynames).
	destName := entryname
	if destName == "" {
		if bn, ok := fileObj["basename"].(string); ok {
			destName = bn
		} else {
			destName = filepath.Base(srcPath)
		}
	}

	destPath := filepath.Join(workDir, destName)

	// Create parent directory if needed (for nested paths).
	if dir := filepath.Dir(destPath); dir != workDir {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create parent dir for %q: %w", destName, err)
		}
	}

	// Record the staging for input path updates.
	if stagedPaths != nil && entryname != "" {
		absSrc, _ := filepath.Abs(srcPath)
		stagedPaths[absSrc] = destPath
	}

	// Copy files when writable (unless inplaceUpdate) or when executing in container.
	// With InplaceUpdateRequirement: writable files are symlinked so modifications affect original.
	shouldCopy := (writable && !inplaceUpdate) || copyForContainer
	if shouldCopy {
		if err := copyFile(srcPath, destPath); err != nil {
			return nil, err
		}
		stageIWDSecondaryFiles(fileObj, workDir, destName, writable, stagedPaths, copyForContainer, inplaceUpdate)
		return nil, nil
	}
	// Non-writable or inplaceUpdate: symlink.
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		absSrc = srcPath
	}
	if err := os.Symlink(absSrc, destPath); err != nil {
		return nil, err
	}
	stageIWDSecondaryFiles(fileObj, workDir, destName, writable, stagedPaths, copyForContainer, inplaceUpdate)
	return nil, nil
}

// stageIWDSecondaryFiles stages secondaryFiles alongside a staged file.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDSecondaryFiles(fileObj map[string]any, workDir, destName string, writable bool, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool) {
	secFiles, ok := fileObj["secondaryFiles"].([]any)
	if !ok {
		return
	}
	for _, sf := range secFiles {
		sfObj, ok := sf.(map[string]any)
		if !ok {
			continue
		}
		sfPath := ""
		if p, ok := sfObj["path"].(string); ok {
			sfPath = p
		} else if loc, ok := sfObj["location"].(string); ok {
			sfPath = strings.TrimPrefix(loc, "file://")
		}
		if sfPath == "" {
			continue
		}
		sfBasename := filepath.Base(sfPath)
		sfDest := filepath.Join(workDir, sfBasename)
		// If the primary file was renamed with entryname, don't rename secondaryFiles.
		// Copy when writable (unless inplaceUpdate) or executing in container.
		shouldCopy := (writable && !inplaceUpdate) || copyForContainer
		if shouldCopy {
			_ = copyFile(sfPath, sfDest)
		} else {
			absSf, _ := filepath.Abs(sfPath)
			_ = os.Symlink(absSf, sfDest)
		}
	}
}

// stageIWDDirectory stages a CWL Directory object into the work directory.
// When copyForContainer is true, directories are copied instead of symlinked (for Docker/Apptainer).
// If entryname is an absolute path and copyForContainer is true, returns a ContainerMount.
// inplaceUpdate: if true, writable entries are symlinked instead of copied (InplaceUpdateRequirement).
func stageIWDDirectory(dirObj map[string]any, entryname string, writable bool, workDir string, stagedPaths map[string]string, copyForContainer bool, inplaceUpdate bool, allowAbsoluteEntryname bool) ([]ContainerMount, error) {
	// Get source path.
	srcPath := ""
	if p, ok := dirObj["path"].(string); ok {
		srcPath = p
	} else if loc, ok := dirObj["location"].(string); ok {
		srcPath = strings.TrimPrefix(loc, "file://")
	}

	// Handle absolute entryname for container execution.
	if copyForContainer && entryname != "" && strings.HasPrefix(entryname, "/") && srcPath != "" {
		absSrc, err := filepath.Abs(srcPath)
		if err != nil {
			absSrc = srcPath
		}
		return []ContainerMount{{
			HostPath:      absSrc,
			ContainerPath: entryname,
			IsDirectory:   true,
		}}, nil
	}

	// Determine destination name.
	destName := entryname
	if destName == "" {
		if bn, ok := dirObj["basename"].(string); ok {
			destName = bn
		} else if srcPath != "" {
			destName = filepath.Base(srcPath)
		}
	}

	destPath := filepath.Join(workDir, destName)

	// Handle Directory with listing but no source path (synthetic directory).
	if srcPath == "" {
		if err := os.MkdirAll(destPath, 0755); err != nil {
			return nil, fmt.Errorf("create directory %q: %w", destName, err)
		}
		// Stage listing contents if present.
		var mounts []ContainerMount
		if listing, ok := dirObj["listing"].([]any); ok {
			for _, item := range listing {
				if fileObj, ok := item.(map[string]any); ok {
					if class, _ := fileObj["class"].(string); class == "File" {
						itemMounts, err := stageIWDFile(fileObj, "", writable, destPath, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
						if err != nil {
							return nil, err
						}
						mounts = append(mounts, itemMounts...)
					} else if class == "Directory" {
						itemMounts, err := stageIWDDirectory(fileObj, "", writable, destPath, stagedPaths, copyForContainer, inplaceUpdate, allowAbsoluteEntryname)
						if err != nil {
							return nil, err
						}
						mounts = append(mounts, itemMounts...)
					}
				}
			}
		}
		return mounts, nil
	}

	// Copy when writable (unless inplaceUpdate) or executing in container.
	// With InplaceUpdateRequirement: writable dirs are symlinked so modifications affect original.
	shouldCopy := (writable && !inplaceUpdate) || copyForContainer
	if shouldCopy {
		return nil, copyDir(srcPath, destPath)
	}

	// Non-writable or inplaceUpdate: symlink.
	absSrc, err := filepath.Abs(srcPath)
	if err != nil {
		absSrc = srcPath
	}
	return nil, os.Symlink(absSrc, destPath)
}

// updateInputPathValue recursively updates paths for staged files.
func updateInputPathValue(v any, workDir string, stagedPaths map[string]string) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && class == "File" {
			if origPath, ok := val["path"].(string); ok {
				// Check if this file was explicitly staged with entryname.
				if newPath, ok := stagedPaths[origPath]; ok {
					val["path"] = newPath
					val["basename"] = filepath.Base(newPath)
					return
				}
				// Otherwise check if staged by basename.
				bn := filepath.Base(origPath)
				stagedPath := filepath.Join(workDir, bn)
				if _, err := os.Lstat(stagedPath); err == nil {
					val["path"] = stagedPath
				}
			}
		}
		if class, ok := val["class"].(string); ok && class == "Directory" {
			if origPath, ok := val["path"].(string); ok {
				if newPath, ok := stagedPaths[origPath]; ok {
					val["path"] = newPath
					val["basename"] = filepath.Base(newPath)
				}
			}
		}
	case []any:
		for _, item := range val {
			updateInputPathValue(item, workDir, stagedPaths)
		}
	}
}

// iwdResultToString converts a value to its string representation for file content.
// Per CWL spec: objects and arrays are JSON-serialized (matching Python json.dumps format),
// numbers/booleans are stringified.
func iwdResultToString(v any) string {
	return cwlexpr.JsonDumps(v)
}

// isURI checks if a string is a URI.
func isURI(s string) bool {
	return len(s) > 5 && (s[:5] == "file:" || s[:5] == "http:" || s[:6] == "https:")
}
