package cli

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/me/gowe/internal/bundle"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/secondaryfiles"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newRunCmd() *cobra.Command {
	var outDir string
	var quiet bool
	var verbose bool // Accepted for compatibility but currently ignored
	var timeout time.Duration
	var noUpload bool

	cmd := &cobra.Command{
		Use:   "run <cwl-file> [job-file]",
		Short: "Execute a CWL workflow and output results",
		Long: `cwltest-compatible runner: bundles CWL, submits to server, waits for
completion, and outputs results as CWL-formatted JSON to stdout.

This command is designed to be compatible with cwltest, the CWL conformance
testing tool. It follows the same interface as cwl-runner.

By default, input files are uploaded to the server and output files are
downloaded after completion. Use --no-upload to use GOWE_PATH_MAP for
shared-filesystem mode instead.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwlPath := args[0]

			// Parse job file if provided.
			var inputs map[string]any
			if len(args) > 1 {
				jobPath := args[1]
				data, err := os.ReadFile(jobPath)
				if err != nil {
					return fmt.Errorf("read job file: %w", err)
				}
				if err := yaml.Unmarshal(data, &inputs); err != nil {
					return fmt.Errorf("parse job file: %w", err)
				}

				// Resolve File/Directory paths relative to job file location.
				// This ensures paths are absolute and 'path' property is set,
				// which is required for CWL expressions like $(inputs.file1.path).
				jobDir, err := filepath.Abs(filepath.Dir(jobPath))
				if err != nil {
					return fmt.Errorf("get job directory: %w", err)
				}
				if resolved, ok := bundle.ResolveFilePaths(inputs, jobDir).(map[string]any); ok {
					inputs = resolved
				}

				if noUpload {
					// Apply path remapping for distributed execution.
					// GOWE_PATH_MAP format: "src1=dst1:src2=dst2"
					if pathMapStr := os.Getenv("GOWE_PATH_MAP"); pathMapStr != "" {
						pathMap := bundle.ParsePathMap(pathMapStr)
						if remapped, ok := bundle.RemapPaths(inputs, pathMap).(map[string]any); ok {
							inputs = remapped
						}
					}
				}
			}

			return runCWL(cwlPath, inputs, outDir, quiet, noUpload, timeout)
		},
	}

	cmd.Flags().StringVar(&outDir, "outdir", "", "Output directory for result files (default: temporary directory)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress progress messages")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Enable verbose output (for cwltest compatibility)")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Execution timeout")
	cmd.Flags().BoolVar(&noUpload, "no-upload", false, "Disable file upload; use GOWE_PATH_MAP for shared-filesystem mode")

	// Mark verbose as hidden since it's mainly for cwltest compatibility.
	_ = cmd.Flags().MarkHidden("verbose")

	return cmd
}

func runCWL(cwlPath string, inputs map[string]any, outDir string, quiet bool, noUpload bool, timeout time.Duration) error {
	// 1. Bundle CWL files.
	if !quiet {
		fmt.Fprintf(os.Stderr, "Bundling %s...\n", cwlPath)
	}

	result, err := bundle.Bundle(cwlPath)
	if err != nil {
		return fmt.Errorf("bundle CWL: %w", err)
	}

	packedCWL := result.Packed

	if noUpload {
		// Apply path remapping to packed CWL if GOWE_PATH_MAP is set.
		// This remaps absolute host paths to container paths for distributed execution.
		if pathMapStr := os.Getenv("GOWE_PATH_MAP"); pathMapStr != "" {
			pathMap := bundle.ParsePathMap(pathMapStr)
			var doc any
			if err := yaml.Unmarshal(result.Packed, &doc); err == nil {
				remapped := bundle.RemapPaths(doc, pathMap)
				if remappedBytes, err := yaml.Marshal(remapped); err == nil {
					packedCWL = remappedBytes
				}
			}
		}
	} else {
		// Upload mode: resolve secondary files and upload inputs to server.
		if inputs != nil {
			// Resolve secondary files before uploading so they get included.
			// Parse the packed CWL to get input definitions with secondaryFiles patterns.
			p := parser.New(slog.Default())
			cwlDir := filepath.Dir(cwlPath)
			if absDir, err := filepath.Abs(cwlDir); err == nil {
				cwlDir = absDir
			}
			if graph, err := p.ParseGraphWithBase(packedCWL, cwlDir); err == nil && graph.Workflow != nil {
				inputs = secondaryfiles.ResolveForInputDefs(graph.Workflow.Inputs, inputs, cwlDir)
			}

			if !quiet {
				fmt.Fprintf(os.Stderr, "Uploading input files...\n")
			}
			inputs, err = uploadInputFiles(inputs, quiet)
			if err != nil {
				return fmt.Errorf("upload input files: %w", err)
			}
		}

		// Upload File defaults embedded in packed CWL.
		packedCWL, err = uploadPackedCWLFiles(packedCWL, quiet)
		if err != nil {
			return fmt.Errorf("upload packed CWL files: %w", err)
		}
	}

	// 2. Create workflow via API.
	if !quiet {
		fmt.Fprintf(os.Stderr, "Creating workflow %s...\n", result.Name)
	}

	wfReq := map[string]any{
		"name": result.Name,
		"cwl":  string(packedCWL),
	}
	wfResp, err := client.Post("/api/v1/workflows/", wfReq)
	if err != nil {
		return fmt.Errorf("create workflow: %w", err)
	}

	var wfData map[string]any
	if err := json.Unmarshal(wfResp.Data, &wfData); err != nil {
		return fmt.Errorf("parse workflow response: %w", err)
	}
	workflowID, ok := wfData["id"].(string)
	if !ok {
		return fmt.Errorf("workflow response missing 'id' field")
	}

	// 3. Create submission via API.
	if !quiet {
		fmt.Fprintf(os.Stderr, "Submitting with workflow ID %s...\n", workflowID)
	}

	subReq := map[string]any{
		"workflow_id": workflowID,
		"inputs":      inputs,
	}
	subResp, err := client.Post("/api/v1/submissions/", subReq)
	if err != nil {
		return fmt.Errorf("create submission: %w", err)
	}

	var subData model.Submission
	if err := json.Unmarshal(subResp.Data, &subData); err != nil {
		return fmt.Errorf("parse submission response: %w", err)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "Submission created: %s\n", subData.ID)
	}

	// 4. Poll until completion or timeout.
	deadline := time.Now().Add(timeout)
	pollInterval := 500 * time.Millisecond
	lastState := subData.State

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for submission %s", subData.ID)
		}

		resp, err := client.Get("/api/v1/submissions/" + subData.ID)
		if err != nil {
			return fmt.Errorf("poll submission: %w", err)
		}

		if err := json.Unmarshal(resp.Data, &subData); err != nil {
			return fmt.Errorf("parse submission poll response: %w", err)
		}

		if subData.State != lastState && !quiet {
			fmt.Fprintf(os.Stderr, "State: %s\n", subData.State)
			lastState = subData.State
		}

		if subData.State.IsTerminal() {
			break
		}

		time.Sleep(pollInterval)
	}

	// 5. Check final state.
	if subData.State == model.SubmissionStateFailed {
		// Print task errors.
		for _, task := range subData.Tasks {
			if task.State == model.TaskStateFailed {
				fmt.Fprintf(os.Stderr, "Task %s (step %s) failed\n", task.ID, task.StepID)
				// Fetch task logs.
				logsResp, err := client.Get(fmt.Sprintf("/api/v1/submissions/%s/tasks/%s/logs", subData.ID, task.ID))
				if err == nil {
					var logs map[string]any
					if json.Unmarshal(logsResp.Data, &logs) == nil {
						if stderr, ok := logs["stderr"].(string); ok && stderr != "" {
							fmt.Fprintf(os.Stderr, "stderr: %s\n", stderr)
						}
					}
				}
			}
		}
		return fmt.Errorf("submission failed")
	}

	if subData.State == model.SubmissionStateCancelled {
		return fmt.Errorf("submission was cancelled")
	}

	// 6. Collect outputs and format as CWL JSON.
	var outputs map[string]any
	if noUpload {
		// Parse output path map for distributed execution.
		// This maps container output paths back to host paths.
		var outputPathMap map[string]string
		if pathMapStr := os.Getenv("GOWE_OUTPUT_PATH_MAP"); pathMapStr != "" {
			outputPathMap = bundle.ParsePathMap(pathMapStr)
		}
		outputs, err = collectOutputs(subData, outDir, outputPathMap)
	} else {
		// Download outputs from server.
		if !quiet {
			fmt.Fprintf(os.Stderr, "Downloading outputs...\n")
		}
		outputs, err = collectOutputsViaDownload(subData, outDir)
	}
	if err != nil {
		return fmt.Errorf("collect outputs: %w", err)
	}

	// 7. Print CWL-formatted JSON to stdout.
	// Use CWL-compliant marshaling to avoid scientific notation for large numbers.
	outputJSON, err := cwl.MarshalCWLOutput(outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	fmt.Println(string(outputJSON))
	return nil
}

// collectOutputs extracts workflow outputs from the completed submission
// and formats them as CWL File/Directory objects.
// outputPathMap translates container paths to host paths for distributed execution.
func collectOutputs(sub model.Submission, outDir string, outputPathMap map[string]string) (map[string]any, error) {
	outputs := make(map[string]any)

	// Create output directory if specified.
	if outDir == "" {
		var err error
		outDir, err = os.MkdirTemp("", "cwl-output-*")
		if err != nil {
			return nil, fmt.Errorf("create temp output dir: %w", err)
		}
	} else {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("create output dir: %w", err)
		}
	}

	// Build a map of task outputs by step ID.
	taskOutputs := make(map[string]map[string]any)
	for _, task := range sub.Tasks {
		taskOutputs[task.StepID] = task.Outputs
	}

	// Process submission-level outputs (which reference task outputs).
	for outID, outVal := range sub.Outputs {
		outputs[outID] = formatCWLOutput(outVal, outDir, outputPathMap)
	}

	// If submission.Outputs is empty, try to derive from final task outputs.
	// This handles single-step workflows where outputs may not be explicitly mapped.
	if len(outputs) == 0 && len(sub.Tasks) > 0 {
		// For single-step workflows, use the task outputs directly.
		for _, task := range sub.Tasks {
			if task.State == model.TaskStateSuccess {
				for outID, outVal := range task.Outputs {
					outputs[outID] = formatCWLOutput(outVal, outDir, outputPathMap)
				}
			}
		}
	}

	return outputs, nil
}

// formatCWLOutput formats a value as a CWL File or Directory object.
// outputPathMap translates container paths to host paths for distributed execution.
func formatCWLOutput(val any, outDir string, outputPathMap map[string]string) any {
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		if class == "File" {
			return formatFileOutput(v, outDir, outputPathMap)
		}
		if class == "Directory" {
			return formatDirectoryOutput(v, outDir, outputPathMap)
		}

		// Check if it's an untyped file reference.
		if loc, ok := v["location"].(string); ok {
			return formatFileOutput(map[string]any{
				"class":    "File",
				"location": loc,
			}, outDir, outputPathMap)
		}

		// Nested structure - recurse.
		result := make(map[string]any)
		for k, innerVal := range v {
			result[k] = formatCWLOutput(innerVal, outDir, outputPathMap)
		}
		return result

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = formatCWLOutput(item, outDir, outputPathMap)
		}
		return result

	case string:
		// Check if this looks like a file path.
		if strings.HasPrefix(v, "file://") || filepath.IsAbs(v) {
			return formatFileOutput(map[string]any{
				"class":    "File",
				"location": v,
			}, outDir, outputPathMap)
		}
		return v

	default:
		return v
	}
}

// formatFileOutput creates a CWL File object with checksum and size.
// outputPathMap translates container paths to host paths for distributed execution.
func formatFileOutput(fileMap map[string]any, outDir string, outputPathMap map[string]string) map[string]any {
	location, _ := fileMap["location"].(string)
	if location == "" {
		return fileMap
	}

	// Normalize location to file path.
	filePath := location
	if strings.HasPrefix(filePath, "file://") {
		filePath = strings.TrimPrefix(filePath, "file://")
	}

	// Translate container path to host path for distributed execution.
	localPath := translateOutputPath(filePath, outputPathMap)

	result := map[string]any{
		"class":    "File",
		"location": "file://" + localPath,
		"path":     localPath,
		"basename": filepath.Base(localPath),
	}

	// Get file info if the file exists locally.
	if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
		result["size"] = info.Size()

		// Compute SHA1 checksum.
		if checksum, err := computeFileChecksum(localPath); err == nil {
			result["checksum"] = "sha1$" + checksum
		}

		// Copy to output directory if different from source.
		destPath := filepath.Join(outDir, filepath.Base(localPath))
		if destPath != localPath {
			if err := copyFile(localPath, destPath); err == nil {
				result["location"] = "file://" + destPath
				result["path"] = destPath
			}
		}
	}

	// Preserve format field if present.
	if format, ok := fileMap["format"].(string); ok && format != "" {
		result["format"] = format
	}

	// Copy secondary files if present.
	if secondaryFiles, ok := fileMap["secondaryFiles"].([]any); ok {
		formatted := make([]any, 0, len(secondaryFiles))
		for _, sf := range secondaryFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				formatted = append(formatted, formatFileOutput(sfMap, outDir, outputPathMap))
			}
		}
		if len(formatted) > 0 {
			result["secondaryFiles"] = formatted
		}
	}

	return result
}

// formatDirectoryOutput creates a CWL Directory object with listing.
// outputPathMap translates container paths to host paths for distributed execution.
func formatDirectoryOutput(dirMap map[string]any, outDir string, outputPathMap map[string]string) map[string]any {
	location, _ := dirMap["location"].(string)
	if location == "" {
		return dirMap
	}

	// Normalize location to file path.
	dirPath := location
	if strings.HasPrefix(dirPath, "file://") {
		dirPath = strings.TrimPrefix(dirPath, "file://")
	}

	// Translate container path to host path for distributed execution.
	localPath := translateOutputPath(dirPath, outputPathMap)

	result := map[string]any{
		"class":    "Directory",
		"location": "file://" + localPath,
		"path":     localPath,
		"basename": filepath.Base(localPath),
	}

	// Include listing if present, recursively formatting each entry.
	if listing, ok := dirMap["listing"].([]any); ok {
		formattedListing := make([]any, 0, len(listing))
		for _, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				formattedListing = append(formattedListing, formatCWLOutput(itemMap, outDir, outputPathMap))
			} else {
				formattedListing = append(formattedListing, item)
			}
		}
		result["listing"] = formattedListing
	} else if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		// If no listing in server response but directory exists, populate it.
		result["listing"] = buildDirectoryListing(localPath, outDir, outputPathMap)
	}

	return result
}

// buildDirectoryListing recursively lists directory contents.
// outputPathMap translates container paths to host paths for distributed execution.
func buildDirectoryListing(dirPath string, outDir string, outputPathMap map[string]string) []any {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return []any{}
	}

	listing := make([]any, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			listing = append(listing, formatDirectoryOutput(map[string]any{
				"class":    "Directory",
				"location": "file://" + entryPath,
			}, outDir, outputPathMap))
		} else {
			listing = append(listing, formatFileOutput(map[string]any{
				"class":    "File",
				"location": "file://" + entryPath,
			}, outDir, outputPathMap))
		}
	}
	return listing
}

// translateOutputPath translates a container path to a host path.
// This is used for distributed execution where output files are on a shared
// filesystem mounted at different paths in containers vs the host.
func translateOutputPath(path string, pathMap map[string]string) string {
	if pathMap == nil || len(pathMap) == 0 {
		return path
	}

	// Try each mapping prefix.
	for containerPrefix, hostPrefix := range pathMap {
		if strings.HasPrefix(path, containerPrefix) {
			return hostPrefix + strings.TrimPrefix(path, containerPrefix)
		}
	}

	return path
}

// computeFileChecksum calculates the SHA1 checksum of a file.
func computeFileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// uploadInputFiles recursively walks input values, uploads File/Directory objects,
// and remaps their locations to server-side file:// URIs.
func uploadInputFiles(inputs map[string]any, quiet bool) (map[string]any, error) {
	result := make(map[string]any)
	for k, v := range inputs {
		uploaded, err := uploadInputValue(v, quiet)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", k, err)
		}
		result[k] = uploaded
	}
	return result, nil
}

// uploadInputValue uploads a single input value, recursing into maps, arrays,
// and File/Directory objects.
func uploadInputValue(val any, quiet bool) (any, error) {
	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)
		switch class {
		case "File":
			return uploadFileInput(v, quiet)
		case "Directory":
			return uploadDirectoryInput(v, quiet)
		default:
			// Recurse into nested maps.
			result := make(map[string]any)
			for k, inner := range v {
				uploaded, err := uploadInputValue(inner, quiet)
				if err != nil {
					return nil, err
				}
				result[k] = uploaded
			}
			return result, nil
		}

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			uploaded, err := uploadInputValue(item, quiet)
			if err != nil {
				return nil, err
			}
			result[i] = uploaded
		}
		return result, nil

	default:
		return val, nil
	}
}

// uploadFileInput uploads a single File object and returns a remapped copy.
func uploadFileInput(fileObj map[string]any, quiet bool) (map[string]any, error) {
	// Determine the local file path to upload.
	localPath := fileLocationToPath(fileObj)
	if localPath == "" {
		return fileObj, nil // No path to upload (e.g., literal content).
	}

	// Check if file exists locally — if not, it may already be a server-side path.
	if _, err := os.Stat(localPath); err != nil {
		return fileObj, nil
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "  Uploading %s\n", filepath.Base(localPath))
	}

	uploadResult, err := client.UploadFile(localPath)
	if err != nil {
		return nil, fmt.Errorf("upload %s: %w", localPath, err)
	}

	// Build remapped File object with server-side location.
	result := make(map[string]any)
	for k, v := range fileObj {
		result[k] = v
	}
	result["location"] = uploadResult.Location
	result["path"] = strings.TrimPrefix(uploadResult.Location, "file://")

	// Upload secondary files if present.
	// Use uploadInputValue (not uploadFileInput) so Directory secondaryFiles
	// are dispatched to uploadDirectoryInput instead of trying os.Open on a dir.
	if secondaryFiles, ok := result["secondaryFiles"].([]any); ok {
		uploaded := make([]any, 0, len(secondaryFiles))
		for _, sf := range secondaryFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				uploadedSF, err := uploadInputValue(sfMap, quiet)
				if err != nil {
					return nil, err
				}
				uploaded = append(uploaded, uploadedSF)
			} else {
				uploaded = append(uploaded, sf)
			}
		}
		result["secondaryFiles"] = uploaded
	}

	return result, nil
}

// uploadDirectoryInput uploads all files in a Directory's listing.
func uploadDirectoryInput(dirObj map[string]any, quiet bool) (map[string]any, error) {
	result := make(map[string]any)
	for k, v := range dirObj {
		result[k] = v
	}

	// If the directory has a listing, upload each entry.
	if listing, ok := result["listing"].([]any); ok {
		uploaded := make([]any, 0, len(listing))
		for _, item := range listing {
			if itemMap, ok := item.(map[string]any); ok {
				uploadedItem, err := uploadInputValue(itemMap, quiet)
				if err != nil {
					return nil, err
				}
				uploaded = append(uploaded, uploadedItem)
			} else {
				uploaded = append(uploaded, item)
			}
		}
		result["listing"] = uploaded
	} else {
		// No listing — try to upload the directory contents by walking the local path.
		localPath := fileLocationToPath(dirObj)
		if localPath != "" {
			if info, err := os.Stat(localPath); err == nil && info.IsDir() {
				listing, err := uploadDirectoryContents(localPath, quiet)
				if err != nil {
					return nil, fmt.Errorf("upload directory %s: %w", localPath, err)
				}
				result["listing"] = listing
			}
		}
	}

	// Remove host-side location/path since listing entries have server-side locations.
	// The worker uses the listing to stage files, not the directory location.
	delete(result, "location")
	delete(result, "path")

	return result, nil
}

// uploadDirectoryContents uploads all files in a local directory and returns
// a CWL listing array with server-side locations.
func uploadDirectoryContents(dirPath string, quiet bool) ([]any, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	listing := make([]any, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			subDir := map[string]any{
				"class":    "Directory",
				"location": "file://" + entryPath,
				"basename": entry.Name(),
			}
			uploaded, err := uploadDirectoryInput(subDir, quiet)
			if err != nil {
				return nil, err
			}
			listing = append(listing, uploaded)
		} else {
			fileObj := map[string]any{
				"class":    "File",
				"location": "file://" + entryPath,
				"basename": entry.Name(),
			}
			uploaded, err := uploadFileInput(fileObj, quiet)
			if err != nil {
				return nil, err
			}
			listing = append(listing, uploaded)
		}
	}
	return listing, nil
}

// fileLocationToPath extracts the local file path from a CWL File/Directory object.
func fileLocationToPath(obj map[string]any) string {
	// Prefer 'path' over 'location'.
	if p, ok := obj["path"].(string); ok && p != "" {
		return strings.TrimPrefix(p, "file://")
	}
	if loc, ok := obj["location"].(string); ok && loc != "" {
		return strings.TrimPrefix(loc, "file://")
	}
	return ""
}

// uploadPackedCWLFiles walks a packed CWL document for File default values
// and uploads them, remapping locations to server-side URIs.
func uploadPackedCWLFiles(packedCWL []byte, quiet bool) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(packedCWL, &doc); err != nil {
		return packedCWL, nil // If we can't parse, return as-is.
	}

	modified, err := uploadCWLDocFiles(doc, quiet)
	if err != nil {
		return nil, err
	}

	result, err := yaml.Marshal(modified)
	if err != nil {
		return nil, fmt.Errorf("marshal modified CWL: %w", err)
	}
	return result, nil
}

// uploadCWLDocFiles recursively walks a CWL document, uploading File objects.
func uploadCWLDocFiles(val any, quiet bool) (any, error) {
	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)
		if class == "File" {
			localPath := fileLocationToPath(v)
			if localPath != "" {
				if _, err := os.Stat(localPath); err == nil {
					return uploadFileInput(v, quiet)
				}
			}
			return v, nil
		}

		result := make(map[string]any)
		for k, inner := range v {
			modified, err := uploadCWLDocFiles(inner, quiet)
			if err != nil {
				return nil, err
			}
			result[k] = modified
		}
		return result, nil

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			modified, err := uploadCWLDocFiles(item, quiet)
			if err != nil {
				return nil, err
			}
			result[i] = modified
		}
		return result, nil

	default:
		return val, nil
	}
}

// collectOutputsViaDownload extracts workflow outputs from the completed submission
// and downloads them from the server, formatting as CWL File/Directory objects.
func collectOutputsViaDownload(sub model.Submission, outDir string) (map[string]any, error) {
	outputs := make(map[string]any)

	// Create output directory if specified.
	if outDir == "" {
		var err error
		outDir, err = os.MkdirTemp("", "cwl-output-*")
		if err != nil {
			return nil, fmt.Errorf("create temp output dir: %w", err)
		}
	} else {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("create output dir: %w", err)
		}
	}

	// Process submission-level outputs.
	for outID, outVal := range sub.Outputs {
		outputs[outID] = downloadAndFormatOutput(outVal, outDir)
	}

	// If submission.Outputs is empty, try to derive from final task outputs.
	if len(outputs) == 0 && len(sub.Tasks) > 0 {
		for _, task := range sub.Tasks {
			if task.State == model.TaskStateSuccess {
				for outID, outVal := range task.Outputs {
					outputs[outID] = downloadAndFormatOutput(outVal, outDir)
				}
			}
		}
	}

	return outputs, nil
}

// downloadAndFormatOutput recursively downloads and formats an output value.
func downloadAndFormatOutput(val any, outDir string) any {
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		if class == "File" {
			return downloadFileOutput(v, outDir)
		}
		if class == "Directory" {
			return downloadDirectoryOutput(v, outDir)
		}

		// Check if it's an untyped file reference.
		if loc, ok := v["location"].(string); ok {
			return downloadFileOutput(map[string]any{
				"class":    "File",
				"location": loc,
			}, outDir)
		}

		// Nested structure - recurse.
		result := make(map[string]any)
		for k, innerVal := range v {
			result[k] = downloadAndFormatOutput(innerVal, outDir)
		}
		return result

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = downloadAndFormatOutput(item, outDir)
		}
		return result

	case string:
		if strings.HasPrefix(v, "file://") || filepath.IsAbs(v) {
			return downloadFileOutput(map[string]any{
				"class":    "File",
				"location": v,
			}, outDir)
		}
		return v

	default:
		return v
	}
}

// downloadFileOutput downloads a file from the server and creates a CWL File object.
func downloadFileOutput(fileMap map[string]any, outDir string) map[string]any {
	location, _ := fileMap["location"].(string)
	if location == "" {
		return fileMap
	}

	basename := filepath.Base(strings.TrimPrefix(location, "file://"))
	destPath := filepath.Join(outDir, basename)

	// Try to download from server.
	if err := client.DownloadFile(location, destPath); err != nil {
		// If download fails, fall back to local path resolution (may be a local file).
		return formatFileOutput(fileMap, outDir, nil)
	}

	result := map[string]any{
		"class":    "File",
		"location": "file://" + destPath,
		"path":     destPath,
		"basename": basename,
	}

	// Get file info for size and checksum.
	if info, err := os.Stat(destPath); err == nil {
		result["size"] = info.Size()
		if checksum, err := computeFileChecksum(destPath); err == nil {
			result["checksum"] = "sha1$" + checksum
		}
	}

	// Preserve format field if present.
	if format, ok := fileMap["format"].(string); ok && format != "" {
		result["format"] = format
	}

	// Download secondary files.
	if secondaryFiles, ok := fileMap["secondaryFiles"].([]any); ok {
		formatted := make([]any, 0, len(secondaryFiles))
		for _, sf := range secondaryFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				formatted = append(formatted, downloadFileOutput(sfMap, outDir))
			}
		}
		if len(formatted) > 0 {
			result["secondaryFiles"] = formatted
		}
	}

	return result
}

// downloadDirectoryOutput downloads directory contents from the server
// and creates a CWL Directory object.
func downloadDirectoryOutput(dirMap map[string]any, outDir string) map[string]any {
	location, _ := dirMap["location"].(string)
	if location == "" {
		return dirMap
	}

	basename := filepath.Base(strings.TrimPrefix(location, "file://"))
	localDir := filepath.Join(outDir, basename)

	result := map[string]any{
		"class":    "Directory",
		"location": "file://" + localDir,
		"path":     localDir,
		"basename": basename,
	}

	// If listing provided in the server response, download each entry.
	if listing, ok := dirMap["listing"].([]any); ok {
		if err := os.MkdirAll(localDir, 0o755); err == nil {
			formattedListing := make([]any, 0, len(listing))
			for _, item := range listing {
				if itemMap, ok := item.(map[string]any); ok {
					formattedListing = append(formattedListing, downloadAndFormatOutput(itemMap, localDir))
				} else {
					formattedListing = append(formattedListing, item)
				}
			}
			result["listing"] = formattedListing
		}
	} else {
		// No listing in response — try to get listing from server.
		entries, err := client.ListDirectory(location)
		if err == nil && len(entries) > 0 {
			if err := os.MkdirAll(localDir, 0o755); err == nil {
				formattedListing := make([]any, 0, len(entries))
				for _, entry := range entries {
					entryLoc, _ := entry["location"].(string)
					isDir, _ := entry["is_dir"].(bool)
					entryBasename, _ := entry["basename"].(string)

					if isDir {
						subDir := map[string]any{
							"class":    "Directory",
							"location": entryLoc,
							"basename": entryBasename,
						}
						formattedListing = append(formattedListing, downloadDirectoryOutput(subDir, localDir))
					} else {
						subFile := map[string]any{
							"class":    "File",
							"location": entryLoc,
							"basename": entryBasename,
						}
						formattedListing = append(formattedListing, downloadFileOutput(subFile, localDir))
					}
				}
				result["listing"] = formattedListing
			}
		}
	}

	return result
}
