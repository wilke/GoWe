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

	"github.com/me/gowe/internal/bundle"
	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newRunCmd() *cobra.Command {
	var outDir string
	var quiet bool
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "run <cwl-file> [job-file]",
		Short: "Execute a CWL workflow and output results",
		Long: `cwltest-compatible runner: bundles CWL, submits to server, waits for
completion, and outputs results as CWL-formatted JSON to stdout.

This command is designed to be compatible with cwltest, the CWL conformance
testing tool. It follows the same interface as cwl-runner.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwlPath := args[0]

			// Parse job file if provided.
			var inputs map[string]any
			if len(args) > 1 {
				data, err := os.ReadFile(args[1])
				if err != nil {
					return fmt.Errorf("read job file: %w", err)
				}
				if err := yaml.Unmarshal(data, &inputs); err != nil {
					return fmt.Errorf("parse job file: %w", err)
				}
			}

			return runCWL(cwlPath, inputs, outDir, quiet, timeout)
		},
	}

	cmd.Flags().StringVar(&outDir, "outdir", "", "Output directory for result files (default: temporary directory)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Suppress progress messages")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Execution timeout")

	return cmd
}

func runCWL(cwlPath string, inputs map[string]any, outDir string, quiet bool, timeout time.Duration) error {
	// 1. Bundle CWL files.
	if !quiet {
		fmt.Fprintf(os.Stderr, "Bundling %s...\n", cwlPath)
	}

	result, err := bundle.Bundle(cwlPath)
	if err != nil {
		return fmt.Errorf("bundle CWL: %w", err)
	}

	// 2. Create workflow via API.
	if !quiet {
		fmt.Fprintf(os.Stderr, "Creating workflow %s...\n", result.Name)
	}

	wfReq := map[string]any{
		"name": result.Name,
		"cwl":  string(result.Packed),
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
	pollInterval := 1 * time.Second
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
	outputs, err := collectOutputs(subData, outDir)
	if err != nil {
		return fmt.Errorf("collect outputs: %w", err)
	}

	// 7. Print CWL-formatted JSON to stdout.
	outputJSON, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal outputs: %w", err)
	}

	fmt.Println(string(outputJSON))
	return nil
}

// collectOutputs extracts workflow outputs from the completed submission
// and formats them as CWL File/Directory objects.
func collectOutputs(sub model.Submission, outDir string) (map[string]any, error) {
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
		outputs[outID] = formatCWLOutput(outVal, outDir)
	}

	// If submission.Outputs is empty, try to derive from final task outputs.
	// This handles single-step workflows where outputs may not be explicitly mapped.
	if len(outputs) == 0 && len(sub.Tasks) > 0 {
		// For single-step workflows, use the task outputs directly.
		for _, task := range sub.Tasks {
			if task.State == model.TaskStateSuccess {
				for outID, outVal := range task.Outputs {
					outputs[outID] = formatCWLOutput(outVal, outDir)
				}
			}
		}
	}

	return outputs, nil
}

// formatCWLOutput formats a value as a CWL File or Directory object.
func formatCWLOutput(val any, outDir string) any {
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)

		if class == "File" {
			return formatFileOutput(v, outDir)
		}
		if class == "Directory" {
			return formatDirectoryOutput(v, outDir)
		}

		// Check if it's an untyped file reference.
		if loc, ok := v["location"].(string); ok {
			return formatFileOutput(map[string]any{
				"class":    "File",
				"location": loc,
			}, outDir)
		}

		// Nested structure - recurse.
		result := make(map[string]any)
		for k, innerVal := range v {
			result[k] = formatCWLOutput(innerVal, outDir)
		}
		return result

	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = formatCWLOutput(item, outDir)
		}
		return result

	case string:
		// Check if this looks like a file path.
		if strings.HasPrefix(v, "file://") || filepath.IsAbs(v) {
			return formatFileOutput(map[string]any{
				"class":    "File",
				"location": v,
			}, outDir)
		}
		return v

	default:
		return v
	}
}

// formatFileOutput creates a CWL File object with checksum and size.
func formatFileOutput(fileMap map[string]any, outDir string) map[string]any {
	location, _ := fileMap["location"].(string)
	if location == "" {
		return fileMap
	}

	// Normalize location to file path.
	filePath := location
	if strings.HasPrefix(filePath, "file://") {
		filePath = strings.TrimPrefix(filePath, "file://")
	}

	result := map[string]any{
		"class":    "File",
		"location": "file://" + filePath,
		"path":     filePath,
		"basename": filepath.Base(filePath),
	}

	// Get file info if the file exists locally.
	if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
		result["size"] = info.Size()

		// Compute SHA1 checksum.
		if checksum, err := computeFileChecksum(filePath); err == nil {
			result["checksum"] = "sha1$" + checksum
		}

		// Copy to output directory if different from source.
		destPath := filepath.Join(outDir, filepath.Base(filePath))
		if destPath != filePath {
			if err := copyFile(filePath, destPath); err == nil {
				result["location"] = "file://" + destPath
				result["path"] = destPath
			}
		}
	}

	// Copy secondary files if present.
	if secondaryFiles, ok := fileMap["secondaryFiles"].([]any); ok {
		formatted := make([]any, 0, len(secondaryFiles))
		for _, sf := range secondaryFiles {
			if sfMap, ok := sf.(map[string]any); ok {
				formatted = append(formatted, formatFileOutput(sfMap, outDir))
			}
		}
		if len(formatted) > 0 {
			result["secondaryFiles"] = formatted
		}
	}

	return result
}

// formatDirectoryOutput creates a CWL Directory object.
func formatDirectoryOutput(dirMap map[string]any, outDir string) map[string]any {
	location, _ := dirMap["location"].(string)
	if location == "" {
		return dirMap
	}

	// Normalize location to file path.
	dirPath := location
	if strings.HasPrefix(dirPath, "file://") {
		dirPath = strings.TrimPrefix(dirPath, "file://")
	}

	result := map[string]any{
		"class":    "Directory",
		"location": "file://" + dirPath,
		"path":     dirPath,
		"basename": filepath.Base(dirPath),
	}

	return result
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
