package cwltool

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/cwlexpr"
	"github.com/me/gowe/internal/fileliteral"
	"github.com/me/gowe/internal/loadcontents"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/staging"
)

// MergeToolDefaults merges tool input defaults with provided inputs.
// Defaults are only used for inputs not provided in the job file.
// Only inputs declared in the tool's inputs are included; undeclared inputs are ignored.
// Also processes loadContents for File inputs (with 64KB limit).
func MergeToolDefaults(tool *cwl.CommandLineTool, inputs map[string]any, cwlDir string) (map[string]any, error) {
	merged := make(map[string]any)

	// First pass: Merge all input values without validation.
	for inputID, inputDef := range tool.Inputs {
		var val any
		if v, exists := inputs[inputID]; exists && v != nil {
			val = v
		} else if inputDef.Default != nil {
			val = ResolveDefaultValue(inputDef.Default, cwlDir)
		} else if v, exists := inputs[inputID]; exists {
			// Explicitly provided null with no default - keep null.
			val = v
		}

		// Process loadContents for File inputs (with 64KB limit).
		if val != nil && inputDef.LoadContents {
			processedVal, err := loadcontents.Process(val, cwlDir)
			if err != nil {
				return nil, fmt.Errorf("input %q: %w", inputID, err)
			}
			val = processedVal
		}

		merged[inputID] = val
	}

	// Second pass: Validate secondaryFiles requirements.
	for inputID, inputDef := range tool.Inputs {
		val := merged[inputID]
		if val != nil {
			if err := secondaryfiles.ValidateInput(inputID, inputDef, val, merged); err != nil {
				return nil, err
			}
		}
	}

	return merged, nil
}

// PopulateDerivedFileProperties ensures that derived CWL properties (dirname,
// basename, nameroot, nameext, size) are populated on all File/Directory objects
// in the inputs map. This is needed in the distributed worker path where inputs
// arrive from the upload pipeline without these derived properties.
func PopulateDerivedFileProperties(inputs map[string]any) {
	for _, v := range inputs {
		populateDerivedProps(v)
	}
}

func populateDerivedProps(v any) {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			if path, ok := val["path"].(string); ok && path != "" {
				if _, has := val["basename"]; !has {
					val["basename"] = filepath.Base(path)
				}
				if _, has := val["dirname"]; !has {
					val["dirname"] = filepath.Dir(path)
				}
				basename := filepath.Base(path)
				if _, has := val["nameroot"]; !has {
					nameroot, nameext := splitBasenameExt(basename)
					val["nameroot"] = nameroot
					val["nameext"] = nameext
				}
				if class == "File" {
					if _, has := val["size"]; !has {
						if info, err := os.Stat(path); err == nil && !info.IsDir() {
							val["size"] = info.Size()
						}
					}
				}
			}
			// Recurse into secondaryFiles.
			if secFiles, ok := val["secondaryFiles"].([]any); ok {
				for _, sf := range secFiles {
					populateDerivedProps(sf)
				}
			}
			// Recurse into listing.
			if listing, ok := val["listing"].([]any); ok {
				for _, item := range listing {
					populateDerivedProps(item)
				}
			}
		} else {
			// Generic map — recurse into values.
			for _, nested := range val {
				populateDerivedProps(nested)
			}
		}
	case []any:
		for _, item := range val {
			populateDerivedProps(item)
		}
	}
}

// ResolveDefaultValue resolves a default value, handling File objects specially.
func ResolveDefaultValue(v any, cwlDir string) any {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok {
			if class == "File" || class == "Directory" {
				return ResolveFileObject(val, cwlDir)
			}
		}
		resolved := make(map[string]any)
		for k, v := range val {
			resolved[k] = ResolveDefaultValue(v, cwlDir)
		}
		return resolved
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = ResolveDefaultValue(item, cwlDir)
		}
		return resolved
	default:
		return v
	}
}

// ResolveFileObject resolves a File or Directory path.
func ResolveFileObject(obj map[string]any, baseDir string) map[string]any {
	resolved := make(map[string]any)
	for k, v := range obj {
		resolved[k] = v
	}

	// Handle file literals: File objects with "contents" but no path/location.
	if _, err := fileliteral.MaterializeFileObject(resolved); err != nil {
		_ = err
	}

	// Step 1: Resolve location (make it absolute if relative).
	if loc, ok := resolved["location"].(string); ok {
		if !filepath.IsAbs(loc) && !isURI(loc) {
			resolved["location"] = filepath.Join(baseDir, loc)
		}
	}

	// Step 2: Resolve path (make it absolute if relative).
	if path, ok := resolved["path"].(string); ok {
		if !filepath.IsAbs(path) {
			resolved["path"] = filepath.Join(baseDir, path)
		}
	}

	// Step 3: If path not set, derive from location.
	if _, hasPath := resolved["path"]; !hasPath {
		if loc, ok := resolved["location"].(string); ok {
			var path string
			if strings.HasPrefix(loc, "file://") {
				path = loc[7:]
			} else if !strings.Contains(loc, "://") {
				path = loc
			}
			if path != "" {
				path = cwl.DecodePath(path)
				resolved["path"] = path
			}
		}
	}

	// Step 4: Final check - ensure path is absolute.
	if path, ok := resolved["path"].(string); ok && !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err == nil {
			resolved["path"] = absPath
		}
	}

	// Step 5: Compute basename, dirname, nameroot, nameext if path is available.
	if path, ok := resolved["path"].(string); ok && path != "" {
		if _, hasBasename := resolved["basename"]; !hasBasename {
			resolved["basename"] = filepath.Base(path)
		}
		if _, hasDirname := resolved["dirname"]; !hasDirname {
			resolved["dirname"] = filepath.Dir(path)
		}
		basename := filepath.Base(path)
		if _, hasNameroot := resolved["nameroot"]; !hasNameroot {
			nameroot, nameext := splitBasenameExt(basename)
			resolved["nameroot"] = nameroot
			resolved["nameext"] = nameext
		}

		// Step 5b: Populate size for File objects if not already set.
		class, _ := resolved["class"].(string)
		if class == "File" {
			if _, hasSize := resolved["size"]; !hasSize {
				if info, err := os.Stat(path); err == nil && !info.IsDir() {
					resolved["size"] = info.Size()
				}
			}
		}
	}

	// Step 6: For Directory objects, resolve listing entries.
	if listing, ok := resolved["listing"].([]any); ok {
		dirPath := baseDir
		if path, ok := resolved["path"].(string); ok {
			dirPath = path
		}
		resolvedListing := make([]any, len(listing))
		for i, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				resolvedListing[i] = ResolveFileObject(itemMap, dirPath)
			} else {
				resolvedListing[i] = item
			}
		}
		resolved["listing"] = resolvedListing
	}

	// Step 7: For File objects, resolve secondaryFiles entries.
	if secFiles, ok := resolved["secondaryFiles"].([]any); ok {
		resolvedSecFiles := make([]any, len(secFiles))
		for i, item := range secFiles {
			if itemMap, ok := item.(map[string]any); ok {
				resolvedSecFiles[i] = ResolveFileObject(itemMap, baseDir)
			} else {
				resolvedSecFiles[i] = item
			}
		}
		resolved["secondaryFiles"] = resolvedSecFiles
	}

	return resolved
}

// PopulateDirectoryListings adds listing entries to Directory inputs based on loadListing.
// When removeDefault is true, listings are removed for the default (empty) loadListing case.
// This should be true for worker/executor paths where inputs come from upload and may have
// upload-created listings that need to be stripped. It should be false for cwl-runner where
// inline directory literals must be preserved.
func PopulateDirectoryListings(tool *cwl.CommandLineTool, inputs map[string]any, removeDefault bool) {
	defaultLoadListing := ""
	if tool.Requirements != nil {
		if llr, ok := tool.Requirements["LoadListingRequirement"].(map[string]any); ok {
			if ll, ok := llr["loadListing"].(string); ok {
				defaultLoadListing = ll
			}
		}
	}

	for inputID, inp := range tool.Inputs {
		loadListing := inp.LoadListing
		if loadListing == "" {
			loadListing = defaultLoadListing
		}
		if loadListing == "no_listing" {
			// Per CWL spec, no_listing means listing should not be present.
			// Remove any listing that was populated during staging/upload.
			removeListingFromInput(inputs, inputID)
			continue
		}
		if loadListing == "" {
			if removeDefault {
				// In worker/executor mode, remove upload-created listings
				// when no loadListing is specified (CWL default is no_listing).
				// Only remove listings that were created by the upload pipeline
				// (marked with _listing_from_upload), not job-provided listings.
				removeUploadListingFromInput(inputs, inputID)
			}
			// In cwl-runner mode, don't remove existing listings;
			// inline document-defined listings (directory literals) must be preserved.
			continue
		}

		inputVal, ok := inputs[inputID]
		if !ok || inputVal == nil {
			continue
		}

		if dirObj, ok := inputVal.(map[string]any); ok {
			if class, _ := dirObj["class"].(string); class == "Directory" {
				PopulateDirListing(dirObj, loadListing)
			}
		}
		if arr, ok := inputVal.([]any); ok {
			for _, item := range arr {
				if dirObj, ok := item.(map[string]any); ok {
					if class, _ := dirObj["class"].(string); class == "Directory" {
						PopulateDirListing(dirObj, loadListing)
					}
				}
			}
		}
	}
}

// removeUploadListingFromInput removes the listing field from Directory inputs
// only if it was created by the upload pipeline (marked with _listing_from_upload).
// This preserves job-provided listings while removing upload artifacts.
func removeUploadListingFromInput(inputs map[string]any, inputID string) {
	inputVal, ok := inputs[inputID]
	if !ok || inputVal == nil {
		return
	}
	if dirObj, ok := inputVal.(map[string]any); ok {
		if class, _ := dirObj["class"].(string); class == "Directory" {
			if _, isUpload := dirObj["_listing_from_upload"]; isUpload {
				delete(dirObj, "listing")
				delete(dirObj, "_listing_from_upload")
			}
		}
	}
	if arr, ok := inputVal.([]any); ok {
		for _, item := range arr {
			if dirObj, ok := item.(map[string]any); ok {
				if class, _ := dirObj["class"].(string); class == "Directory" {
					if _, isUpload := dirObj["_listing_from_upload"]; isUpload {
						delete(dirObj, "listing")
						delete(dirObj, "_listing_from_upload")
					}
				}
			}
		}
	}
}

// removeListingFromInput removes the listing field from Directory inputs.
func removeListingFromInput(inputs map[string]any, inputID string) {
	inputVal, ok := inputs[inputID]
	if !ok || inputVal == nil {
		return
	}
	if dirObj, ok := inputVal.(map[string]any); ok {
		if class, _ := dirObj["class"].(string); class == "Directory" {
			delete(dirObj, "listing")
		}
	}
	if arr, ok := inputVal.([]any); ok {
		for _, item := range arr {
			if dirObj, ok := item.(map[string]any); ok {
				if class, _ := dirObj["class"].(string); class == "Directory" {
					delete(dirObj, "listing")
				}
			}
		}
	}
}

// PopulateDirListing reads the filesystem and populates a Directory object's listing.
func PopulateDirListing(dirObj map[string]any, depth string) {
	dirPath, _ := dirObj["path"].(string)
	if dirPath == "" {
		return
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return
	}

	var listing []any
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			child := map[string]any{
				"class":    "Directory",
				"location": "file://" + entryPath,
				"path":     entryPath,
				"basename": entry.Name(),
			}
			if depth == "deep_listing" {
				PopulateDirListing(child, depth)
			}
			listing = append(listing, child)
		} else {
			info, err := entry.Info()
			size := int64(0)
			if err == nil {
				size = info.Size()
			}
			child := map[string]any{
				"class":    "File",
				"location": "file://" + entryPath,
				"path":     entryPath,
				"basename": entry.Name(),
				"size":     size,
			}
			listing = append(listing, child)
		}
	}
	dirObj["listing"] = listing
}

// DetectContainerRuntime probes the system for available container runtimes.
// Returns "docker" if docker is found, "apptainer" if apptainer is found,
// or "" if neither is available (falls through to local execution).
func DetectContainerRuntime() string {
	if _, err := exec.LookPath("docker"); err == nil {
		return "docker"
	}
	if _, err := exec.LookPath("apptainer"); err == nil {
		return "apptainer"
	}
	return ""
}

// HasDockerRequirement checks if a tool has a DockerRequirement.
func HasDockerRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements != nil {
		if _, ok := tool.Requirements["DockerRequirement"]; ok {
			return true
		}
	}
	if tool.Hints != nil {
		if _, ok := tool.Hints["DockerRequirement"]; ok {
			return true
		}
	}
	return false
}

// GetDockerImage extracts the Docker image from requirements or hints.
func GetDockerImage(tool *cwl.CommandLineTool) string {
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if pull, ok := dr["dockerPull"].(string); ok {
				return pull
			}
		}
	}
	return ""
}

// GetDockerOutputDirectory extracts dockerOutputDirectory from DockerRequirement.
func GetDockerOutputDirectory(tool *cwl.CommandLineTool) string {
	if tool.Requirements != nil {
		if dr, ok := tool.Requirements["DockerRequirement"].(map[string]any); ok {
			if outputDir, ok := dr["dockerOutputDirectory"].(string); ok {
				return outputDir
			}
		}
	}
	if tool.Hints != nil {
		if dr, ok := tool.Hints["DockerRequirement"].(map[string]any); ok {
			if outputDir, ok := dr["dockerOutputDirectory"].(string); ok {
				return outputDir
			}
		}
	}
	return ""
}

// HasInplaceUpdateRequirement checks if InplaceUpdateRequirement is enabled.
func HasInplaceUpdateRequirement(tool *cwl.CommandLineTool) bool {
	if tool.Requirements == nil {
		return false
	}
	iur, ok := tool.Requirements["InplaceUpdateRequirement"].(map[string]any)
	if !ok {
		return false
	}
	inplaceUpdate, _ := iur["inplaceUpdate"].(bool)
	return inplaceUpdate
}

// GetToolTimeLimit extracts the ToolTimeLimit timelimit value in seconds.
// Supports expression evaluation for dynamic timelimit values.
func GetToolTimeLimit(tool *cwl.CommandLineTool, inputs map[string]any) int {
	if tool.Requirements == nil {
		return 0
	}
	ttl, ok := tool.Requirements["ToolTimeLimit"].(map[string]any)
	if !ok {
		return 0
	}
	limit, ok := ttl["timelimit"]
	if !ok {
		return 0
	}

	var result int
	switch v := limit.(type) {
	case int:
		result = v
	case float64:
		result = int(v)
	case string:
		if cwlexpr.IsExpression(v) {
			expressionLib := ExtractExpressionLibFromTool(tool)
			evaluator := cwlexpr.NewEvaluator(expressionLib)
			ctx := cwlexpr.NewContext(inputs)
			evaluated, err := evaluator.Evaluate(v, ctx)
			if err != nil {
				return 0
			}
			switch ev := evaluated.(type) {
			case int:
				result = ev
			case int64:
				result = int(ev)
			case float64:
				result = int(ev)
			default:
				return 0
			}
		}
	}

	return result
}

// ExtractExpressionLibFromTool extracts expression library from a single tool.
func ExtractExpressionLibFromTool(tool *cwl.CommandLineTool) []string {
	if tool.Requirements == nil {
		return nil
	}
	ijsReq, ok := tool.Requirements["InlineJavascriptRequirement"].(map[string]any)
	if !ok {
		return nil
	}
	lib, ok := ijsReq["expressionLib"].([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, item := range lib {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// BuildRuntimeContextWithInputs creates a RuntimeContext, evaluating expressions if inputs are provided.
func BuildRuntimeContextWithInputs(tool *cwl.CommandLineTool, outDir string, inputs map[string]any, expressionLib []string) *cwlexpr.RuntimeContext {
	runtime := cwlexpr.DefaultRuntimeContext()
	runtime.OutDir = outDir
	runtime.TmpDir = outDir + "_tmp"

	rr := getResourceRequirement(tool)
	if rr != nil {
		var evaluator *cwlexpr.Evaluator
		if inputs != nil {
			evaluator = cwlexpr.NewEvaluator(expressionLib)
		}
		ctx := cwlexpr.NewContext(inputs)

		if coresMin, ok := rr["coresMin"]; ok {
			runtime.Cores = evalResourceInt(coresMin, evaluator, ctx)
		}
		if runtime.Cores == 1 {
			if cores, ok := rr["cores"]; ok {
				runtime.Cores = evalResourceInt(cores, evaluator, ctx)
			}
		}

		if ramMin, ok := rr["ramMin"]; ok {
			runtime.Ram = int64(evalResourceInt(ramMin, evaluator, ctx))
		}

		if tmpdirMin, ok := rr["tmpdirMin"]; ok {
			runtime.TmpdirSize = int64(evalResourceInt(tmpdirMin, evaluator, ctx))
		}

		if outdirMin, ok := rr["outdirMin"]; ok {
			runtime.OutdirSize = int64(evalResourceInt(outdirMin, evaluator, ctx))
		}
	}

	return runtime
}

// StageRenamedInputs creates symlinks for File inputs where basename differs from the actual filename.
func StageRenamedInputs(inputs map[string]any, workDir string) error {
	for _, v := range inputs {
		if err := stageRenamedValue(v, workDir); err != nil {
			return err
		}
	}
	return nil
}

// stageRenamedValue recursively processes a value, staging renamed files.
func stageRenamedValue(v any, workDir string) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return stageRenamedFileOrDirectory(val, workDir)
		}
		for _, nested := range val {
			if err := stageRenamedValue(nested, workDir); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range val {
			if err := stageRenamedValue(item, workDir); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageRenamedFileOrDirectory handles a single File or Directory with renamed basename.
func stageRenamedFileOrDirectory(obj map[string]any, stageDir string) error {
	basename, hasBasename := obj["basename"].(string)
	path, hasPath := obj["path"].(string)

	secondaryStaged := false

	if secFiles, ok := obj["secondaryFiles"].([]any); ok {
		for _, sf := range secFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				sfBasename, _ := sfMap["basename"].(string)
				sfPath, _ := sfMap["path"].(string)
				if sfBasename != "" && sfPath != "" && sfBasename != filepath.Base(sfPath) {
					secondaryStaged = true
				}
				if err := stageRenamedFileOrDirectory(sfMap, stageDir); err != nil {
					return err
				}
			}
		}
	}

	if !hasPath || !hasBasename || path == "" || basename == "" {
		return nil
	}

	actualBasename := filepath.Base(path)
	needsRename := basename != actualBasename

	if !needsRename && !secondaryStaged {
		return nil
	}

	if !needsRename && secondaryStaged {
		linkPath := filepath.Join(stageDir, basename)
		if _, err := os.Lstat(linkPath); err == nil {
			existingTarget, readErr := os.Readlink(linkPath)
			if readErr == nil && existingTarget == path {
				obj["path"] = linkPath
				obj["dirname"] = stageDir
				return nil
			}
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("remove stale symlink %s: %w", linkPath, err)
			}
		}
		if err := os.Symlink(path, linkPath); err != nil {
			return fmt.Errorf("symlink %s -> %s: %w", linkPath, path, err)
		}
		obj["path"] = linkPath
		obj["dirname"] = stageDir
		return nil
	}

	linkPath := filepath.Join(stageDir, basename)
	if _, err := os.Lstat(linkPath); err == nil {
		existingTarget, readErr := os.Readlink(linkPath)
		if readErr == nil && existingTarget == path {
			obj["path"] = linkPath
			obj["dirname"] = stageDir
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove stale symlink %s: %w", linkPath, err)
		}
	}

	if err := os.Symlink(path, linkPath); err != nil {
		return fmt.Errorf("symlink %s -> %s: %w", linkPath, path, err)
	}

	obj["path"] = linkPath
	obj["dirname"] = stageDir
	return nil
}

// --- Internal helpers ---

func getResourceRequirement(tool *cwl.CommandLineTool) map[string]any {
	if tool.Requirements != nil {
		if rr, ok := tool.Requirements["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	if tool.Hints != nil {
		if rr, ok := tool.Hints["ResourceRequirement"].(map[string]any); ok {
			return rr
		}
	}
	return nil
}

func evalResourceInt(value any, evaluator *cwlexpr.Evaluator, ctx *cwlexpr.Context) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(math.Ceil(v))
	case string:
		if evaluator != nil && ctx != nil && (strings.HasPrefix(v, "$(") || strings.HasPrefix(v, "${")) {
			result, err := evaluator.Evaluate(v, ctx)
			if err == nil {
				return evalResourceInt(result, nil, nil)
			}
		}
	}
	return 1
}

func splitBasenameExt(basename string) (string, string) {
	for i := len(basename) - 1; i > 0; i-- {
		if basename[i] == '.' {
			return basename[:i], basename[i:]
		}
	}
	return basename, ""
}

func isURI(s string) bool {
	return len(s) > 5 && (s[:5] == "file:" || s[:5] == "http:" || s[:6] == "https:")
}

// stageOutOfVolumeInputs copies input files that are outside the named volume
// into workDir so they are accessible via the Docker named volume. This is
// needed for volume mode (DockerVolume) where the tool container only has
// access to files under the named volume mount point.
//
// Files already under the volume mount point are skipped (they are already
// accessible in the tool container). For out-of-volume files (e.g., default
// files from the CWL directory), symlinks are created if source and dest are
// on the same filesystem; otherwise files are copied.
func stageOutOfVolumeInputs(inputs map[string]any, workDir string) error {
	// Compute the volume mount point by walking up from workDir to the
	// root-level directory (e.g., /workdir/scratch/task_xxx → /workdir).
	volumeRoot := workDir
	for {
		parent := filepath.Dir(volumeRoot)
		if parent == "/" || parent == "." {
			break
		}
		volumeRoot = parent
	}

	for _, v := range inputs {
		if err := stageOutOfVolumeValue(v, workDir, volumeRoot); err != nil {
			return err
		}
	}
	return nil
}

// stageOutOfVolumeValue recursively stages File/Directory values outside the volume.
func stageOutOfVolumeValue(v any, workDir, volumeRoot string) error {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			if err := stageOutOfVolumeFileObj(val, workDir, volumeRoot); err != nil {
				return err
			}
			// Recurse into secondaryFiles.
			if secFiles, ok := val["secondaryFiles"].([]any); ok {
				for _, sf := range secFiles {
					if err := stageOutOfVolumeValue(sf, workDir, volumeRoot); err != nil {
						return err
					}
				}
			}
			// Recurse into listing.
			if listing, ok := val["listing"].([]any); ok {
				for _, item := range listing {
					if err := stageOutOfVolumeValue(item, workDir, volumeRoot); err != nil {
						return err
					}
				}
			}
			return nil
		}
		// Recurse into record fields.
		for _, item := range val {
			if err := stageOutOfVolumeValue(item, workDir, volumeRoot); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range val {
			if err := stageOutOfVolumeValue(item, workDir, volumeRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageOutOfVolumeFileObj stages a single File/Directory object if its path
// is outside the volume mount point. Updates the path/location in-place.
func stageOutOfVolumeFileObj(obj map[string]any, workDir, volumeRoot string) error {
	path, _ := obj["path"].(string)
	if path == "" {
		path, _ = obj["location"].(string)
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil
	}

	// If already under the volume mount point, no staging needed — the file
	// is already accessible in the tool container via the shared volume.
	if strings.HasPrefix(path, volumeRoot+"/") || path == volumeRoot {
		return nil
	}

	// Determine destination inside workDir, preserving the basename.
	basename := filepath.Base(path)
	dest := filepath.Join(workDir, basename)

	// Handle name conflicts by adding a suffix.
	if _, err := os.Lstat(dest); err == nil {
		// Destination exists — check if it's already pointing to the same source.
		if target, err := os.Readlink(dest); err == nil && target == path {
			// Already staged, update paths.
			obj["path"] = dest
			if _, ok := obj["location"]; ok {
				obj["location"] = dest
			}
			return nil
		}
		// Use a unique suffix.
		ext := filepath.Ext(basename)
		name := strings.TrimSuffix(basename, ext)
		for i := 1; ; i++ {
			dest = filepath.Join(workDir, fmt.Sprintf("%s_%d%s", name, i, ext))
			if _, err := os.Lstat(dest); os.IsNotExist(err) {
				break
			}
		}
	}

	// Create symlink (preferred: no data copy, works on same filesystem).
	if err := os.Symlink(path, dest); err != nil {
		// Symlink failed; try copying instead.
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fmt.Errorf("stat %s: %w", path, statErr)
		}
		if info.IsDir() {
			// For directories, symlink is the only option.
			return fmt.Errorf("symlink directory %s -> %s: %w", path, dest, err)
		}
		if copyErr := staging.CopyFile(path, dest); copyErr != nil {
			return fmt.Errorf("copy %s -> %s: %w", path, dest, copyErr)
		}
	}

	// Update paths in-place.
	obj["path"] = dest
	if _, ok := obj["location"]; ok {
		obj["location"] = dest
	}

	// Update derived properties.
	obj["basename"] = filepath.Base(dest)
	obj["dirname"] = filepath.Dir(dest)

	return nil
}
