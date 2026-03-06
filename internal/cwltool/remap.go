package cwltool

import (
	"path/filepath"
	"strings"
)

// RemapInputPaths applies path mapping to all file paths in the inputs.
// This translates host paths (from submitted tasks) to local container paths.
func RemapInputPaths(inputs map[string]any, pathMap map[string]string) map[string]any {
	if len(pathMap) == 0 {
		return inputs
	}
	result, _ := remapPaths(inputs, pathMap).(map[string]any)
	return result
}

// remapPaths recursively remaps file paths in a value using the given path map.
func remapPaths(v any, pathMap map[string]string) any {
	if len(pathMap) == 0 {
		return v
	}

	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			result[k] = remapPaths(v, pathMap)
		}

		class, _ := result["class"].(string)
		if class == "File" || class == "Directory" {
			if loc, ok := result["location"].(string); ok {
				result["location"] = remapPath(loc, pathMap)
			}
			if path, ok := result["path"].(string); ok {
				result["path"] = remapPath(path, pathMap)
			}
		}
		return result

	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = remapPaths(item, pathMap)
		}
		return result

	case string:
		if filepath.IsAbs(val) {
			return remapPath(val, pathMap)
		}
		return val

	default:
		return v
	}
}

// remapPath applies path mapping to a single path string.
func remapPath(path string, pathMap map[string]string) string {
	for srcPrefix, dstPrefix := range pathMap {
		if strings.HasPrefix(path, srcPrefix) {
			return dstPrefix + strings.TrimPrefix(path, srcPrefix)
		}
	}
	return path
}
