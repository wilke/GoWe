package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/me/gowe/internal/bvbrc"
	bvbrcpkg "github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
)

// reservedKeys are internal keys stripped from params before sending to BV-BRC.
var reservedKeys = map[string]bool{
	"_base_command": true,
	"_output_globs": true,
	"_docker_image": true,
	"_bvbrc_app_id": true,
}

// BVBRCExecutor submits and monitors bioinformatics jobs on BV-BRC
// via JSON-RPC 1.1. Submit is async — it returns a job UUID immediately
// and the scheduler polls Status until terminal.
//
// The executor supports two modes:
//  1. Default caller mode: Uses a preconfigured RPC caller for all operations.
//     This is used for status checks and log retrieval.
//  2. Per-task token mode: Creates a per-task caller using the user's token
//     from RuntimeHints.StagerOverrides.HTTPCredential. This is used for
//     job submission to run under the user's identity.
type BVBRCExecutor struct {
	appServiceURL string          // BV-BRC App Service endpoint
	workspaceURL  string          // BV-BRC Workspace endpoint (wildcard-glob output resolution)
	defaultCaller bvbrc.RPCCaller // Optional: default caller for status/logs
	logger        *slog.Logger
}

// NewBVBRCExecutor creates a BVBRCExecutor.
// The defaultCaller is optional and used for status checks and log retrieval.
// If nil, per-task tokens will be required for all operations.
func NewBVBRCExecutor(appServiceURL string, defaultCaller bvbrc.RPCCaller, logger *slog.Logger) *BVBRCExecutor {
	if appServiceURL == "" {
		appServiceURL = bvbrc.DefaultAppServiceURL
	}
	return &BVBRCExecutor{
		appServiceURL: appServiceURL,
		workspaceURL:  bvbrcpkg.DefaultWorkspaceURL,
		defaultCaller: defaultCaller,
		logger:        logger.With("component", "bvbrc-executor"),
	}
}

// SetWorkspaceURL overrides the BV-BRC Workspace service URL used to list a
// result folder when resolving wildcard-glob outputs (buildOutputsFromGlobs).
// Defaults to bvbrc.DefaultWorkspaceURL, matching how appServiceURL is wired
// at this executor's call site today (cmd/server/main.go hardcodes
// bvbrc.DefaultAppServiceURL rather than threading a flag through). Nothing
// currently calls this in production; tests use it to point at a fake
// Workspace service.
func (e *BVBRCExecutor) SetWorkspaceURL(url string) {
	if url != "" {
		e.workspaceURL = url
	}
}

// taskToken extracts the user's BV-BRC token from a task's RuntimeHints, if
// one was set (RuntimeHints.StagerOverrides.HTTPCredential.Token). Returns ""
// when no per-task token is available.
func taskToken(task *model.Task) string {
	if task.RuntimeHints != nil &&
		task.RuntimeHints.StagerOverrides != nil &&
		task.RuntimeHints.StagerOverrides.HTTPCredential != nil {
		return task.RuntimeHints.StagerOverrides.HTTPCredential.Token
	}
	return ""
}

// getTaskCaller creates an RPC caller for the given task.
// It uses the token from RuntimeHints.StagerOverrides.HTTPCredential if available,
// otherwise falls back to the default caller.
func (e *BVBRCExecutor) getTaskCaller(task *model.Task) (bvbrc.RPCCaller, string, error) {
	token := taskToken(task)

	if token != "" {
		// Create per-task caller with user's token.
		cfg := bvbrc.ClientConfig{
			AppServiceURL: e.appServiceURL,
			Token:         token,
		}
		tokenInfo := bvbrc.ParseToken(token)
		return bvbrc.NewHTTPRPCCaller(cfg, e.logger), tokenInfo.Username, nil
	}

	// Fall back to default caller.
	if e.defaultCaller != nil {
		return e.defaultCaller, "", nil
	}

	return nil, "", fmt.Errorf("task %s: no user token for BV-BRC submission", task.ID)
}

// Type returns model.ExecutorTypeBVBRC.
func (e *BVBRCExecutor) Type() model.ExecutorType {
	return model.ExecutorTypeBVBRC
}

// Submit calls AppService.start_app and returns the BV-BRC job UUID.
// The call returns immediately; the job runs asynchronously on BV-BRC.
// The job is submitted using the user's token from RuntimeHints.
func (e *BVBRCExecutor) Submit(ctx context.Context, task *model.Task) (string, error) {
	appID := task.BVBRCAppID
	if appID == "" {
		if v, ok := task.Inputs["_bvbrc_app_id"].(string); ok && v != "" {
			appID = v
		}
	}
	if appID == "" {
		return "", fmt.Errorf("task %s: bvbrc_app_id is missing", task.ID)
	}

	// Get caller for this task (per-task token or default).
	caller, username, err := e.getTaskCaller(task)
	if err != nil {
		return "", err
	}

	// Build params: copy task inputs, stripping reserved keys and null values,
	// and resolving File/Directory CWL objects to workspace path strings.
	// BV-BRC Perl apps expect plain workspace paths, not CWL objects.
	params := make(map[string]any, len(task.Inputs))
	for k, v := range task.Inputs {
		if reservedKeys[k] {
			continue
		}
		if v == nil {
			continue
		}
		resolved := resolveBVBRCInput(v)
		if resolved == nil {
			continue
		}
		params[k] = resolved
	}

	// Determine workspace path from params or default.
	workspacePath, _ := params["output_path"].(string)
	if workspacePath == "" && username != "" {
		// username is the raw "un=" token field, verbatim — it already
		// carries whatever domain suffix the token issuer put there (e.g.
		// "awilke@bvbrc"). Appending another domain here produced a
		// double-domain path ("/awilke@bvbrc@patricbrc.org/home/") that
		// BV-BRC rejects outright; see the #154/#198 promotion round-trip.
		workspacePath = fmt.Sprintf("/%s/home/", username)
	}

	e.logger.Debug("submitting job",
		"task_id", task.ID,
		"app_id", appID,
		"workspace", workspacePath,
		"username", username,
	)

	result, err := caller.Call(ctx, "AppService.start_app", []any{appID, params, workspacePath})
	if err != nil {
		return "", fmt.Errorf("task %s: start_app: %w", task.ID, err)
	}

	// Response: result is [{id, status, ...}] where id may be a number or string.
	var jobs []map[string]any
	if err := json.Unmarshal(result, &jobs); err != nil {
		return "", fmt.Errorf("task %s: parse start_app response: %w", task.ID, err)
	}
	if len(jobs) == 0 {
		return "", fmt.Errorf("task %s: start_app returned empty result", task.ID)
	}

	// BV-BRC returns numeric job IDs; Go JSON decodes them as float64.
	// Format as integer string to avoid scientific notation (e.g. "2.1e+07").
	var jobID string
	switch id := jobs[0]["id"].(type) {
	case float64:
		jobID = strconv.FormatInt(int64(id), 10)
	case json.Number:
		jobID = id.String()
	default:
		jobID = fmt.Sprintf("%v", id)
	}
	e.logger.Info("job submitted",
		"task_id", task.ID,
		"bvbrc_job_id", jobID,
		"bvbrc_status", jobs[0]["status"],
	)

	return jobID, nil
}

// Status calls AppService.query_tasks and maps the BV-BRC status to a TaskState.
// When a job completes successfully, it also fetches the job result to populate
// task.Outputs with the output file list from the workspace.
func (e *BVBRCExecutor) Status(ctx context.Context, task *model.Task) (model.TaskState, error) {
	if task.ExternalID == "" {
		return model.TaskStateQueued, nil
	}

	// Get caller for this task.
	caller, _, err := e.getTaskCaller(task)
	if err != nil {
		// If no caller available, report as queued.
		e.logger.Debug("no caller for status check", "task_id", task.ID, "error", err)
		return model.TaskStateQueued, nil
	}

	result, err := caller.Call(ctx, "AppService.query_tasks", []any{[]string{task.ExternalID}})
	if err != nil {
		return "", fmt.Errorf("task %s: query_tasks: %w", task.ID, err)
	}

	// Response: result is [{jobID: {id, status, output_files, parameters, ...}}]
	var results []map[string]json.RawMessage
	if err := json.Unmarshal(result, &results); err != nil {
		return "", fmt.Errorf("task %s: parse query_tasks response: %w", task.ID, err)
	}
	if len(results) == 0 {
		return model.TaskStateQueued, nil
	}

	raw, ok := results[0][task.ExternalID]
	if !ok {
		return model.TaskStateQueued, nil
	}

	var jobInfo struct {
		Status      string     `json:"status"`
		OutputFiles [][]string `json:"output_files"` // [[ws_path, uuid], ...]
		Parameters  struct {
			OutputPath string `json:"output_path"`
			OutputFile string `json:"output_file"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &jobInfo); err != nil {
		return "", fmt.Errorf("task %s: parse job info: %w", task.ID, err)
	}

	state := mapBVBRCState(jobInfo.Status)

	// On success, build outputs from the output_files list.
	// If output_files is empty (some BV-BRC apps don't populate it),
	// fall back to constructing outputs from CWL glob patterns.
	if state == model.TaskStateSuccess {
		if len(jobInfo.OutputFiles) > 0 {
			outputs := e.buildOutputs(task, jobInfo.OutputFiles, jobInfo.Parameters.OutputPath, jobInfo.Parameters.OutputFile)
			if len(outputs) > 0 {
				task.Outputs = outputs
			}
		} else if len(task.Outputs) == 0 {
			outputs, globErr := e.buildOutputsFromGlobs(ctx, task, jobInfo.Parameters.OutputPath, jobInfo.Parameters.OutputFile)
			if globErr != nil {
				// Propagated like any other error on this path (query_tasks
				// unmarshal, RPC failures, ...): the caller logs it and
				// leaves the task QUEUED for the next poll. Unlike those,
				// this condition (a non-array output whose glob matches more
				// than one file) is permanent for a given job, so the task
				// will keep re-polling into this same error rather than ever
				// reaching a terminal state — that mirrors the existing
				// Status() error-handling convention, not a new one.
				return state, fmt.Errorf("task %s: resolve outputs from glob patterns: %w", task.ID, globErr)
			}
			if len(outputs) > 0 {
				task.Outputs = outputs
				e.logger.Info("built outputs from glob patterns (output_files empty)",
					"task_id", task.ID, "output_count", len(outputs))
			}
		}
	}

	return state, nil
}

// buildOutputs maps BV-BRC output_files to CWL output IDs using the tool's
// output declarations. Files are referenced as ws:// URIs.
func (e *BVBRCExecutor) buildOutputs(task *model.Task, outputFiles [][]string, outputPath, outputFile string) map[string]any {
	outputs := make(map[string]any)

	// Build result_folder from the hidden output directory.
	resultFolder := outputPath + "/." + outputFile
	outputs["result_folder"] = map[string]any{
		"class":    "Directory",
		"location": "ws://" + resultFolder,
		"basename": "." + outputFile,
	}

	// Collect all output files as a listing for the result_folder.
	var listing []any
	for _, entry := range outputFiles {
		if len(entry) < 1 {
			continue
		}
		wsPath := entry[0]
		basename := wsPath[strings.LastIndex(wsPath, "/")+1:]

		fileObj := map[string]any{
			"class":    "File",
			"location": "ws://" + wsPath,
			"basename": basename,
		}
		listing = append(listing, fileObj)

		// Try to match this file to a declared CWL output by glob pattern.
		if task.Tool != nil {
			if matched := matchOutputByGlob(task.Tool, basename, outputFile); matched != "" {
				outputs[matched] = fileObj
			}
		}
	}

	if len(listing) > 0 {
		outputs["result_folder"].(map[string]any)["listing"] = listing
	}

	return outputs
}

// buildOutputsFromGlobs constructs CWL outputs from the tool's glob patterns
// when BV-BRC query_tasks doesn't return output_files. Each concrete (non-
// wildcard) glob pattern is resolved directly to a ws:// URI under the
// result folder. Wildcard globs (containing *?[) are resolved by listing the
// result folder once via Workspace.ls and matching each pattern against the
// listing; see resolveWildcardOutputs.
func (e *BVBRCExecutor) buildOutputsFromGlobs(ctx context.Context, task *model.Task, outputPath, outputFile string) (map[string]any, error) {
	if outputPath == "" || outputFile == "" || task.Tool == nil {
		return nil, nil
	}

	resultFolder := outputPath + "/." + outputFile
	outputs := make(map[string]any)

	outputs["result_folder"] = map[string]any{
		"class":    "Directory",
		"location": "ws://" + resultFolder,
		"basename": "." + outputFile,
	}

	// wildcardOutput defers resolution of a glob containing *?[ until the
	// result folder has been listed; def is the output's own CWL definition
	// (outputBinding + type), reused by globMatches and outputTypeIsArray.
	type wildcardOutput struct {
		id      string
		pattern string
		def     map[string]any
	}
	var wildcards []wildcardOutput

	// Iterate CWL outputs and resolve concrete glob patterns to workspace
	// paths; queue wildcard patterns for resolveWildcardOutputs below.
	iterateOutputGlobs(task.Tool, func(id, glob string, def map[string]any) {
		if id == "result_folder" || id == "result" || glob == "." {
			return
		}
		pattern := strings.ReplaceAll(glob, "$(inputs.output_file)", outputFile)
		if strings.ContainsAny(pattern, "*?[") {
			wildcards = append(wildcards, wildcardOutput{id: id, pattern: pattern, def: def})
			return
		}
		outputs[id] = map[string]any{
			"class":    "File",
			"location": "ws://" + resultFolder + "/" + pattern,
			"basename": pattern,
		}
	})

	if len(wildcards) == 0 {
		return outputs, nil
	}

	listing, err := e.listResultFolder(ctx, task, resultFolder)
	if err != nil {
		return nil, fmt.Errorf("list result folder %s for wildcard-glob outputs: %w", resultFolder, err)
	}
	if len(listing) == 0 {
		// BV-BRC result folders can be slow to index; a genuinely empty
		// listing right after completion is expected, not an error. This
		// Status() call runs once, in the SUCCESS branch, right before the
		// task terminalizes — there is no next poll for these outputs to
		// resolve on, so the skip is permanent for this task, matching the
		// pre-existing behavior for wildcard globs (they were unconditionally
		// skipped before this change too).
		e.logger.Warn("empty workspace listing while resolving wildcard-glob outputs; leaving them unresolved",
			"task_id", task.ID, "result_folder", resultFolder, "wildcard_outputs", len(wildcards))
		return outputs, nil
	}

	for _, w := range wildcards {
		var matches []string
		for _, obj := range listing {
			if obj.Type == bvbrcpkg.WorkspaceTypeFolder || obj.Type == bvbrcpkg.WorkspaceTypeJobResult {
				continue // a glob resolves to files, not subfolders/job-result containers of the result dir
			}
			name := obj.Name
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				continue // defensive: an unusable name from the service
			}
			if globMatches(w.def, name, outputFile) {
				matches = append(matches, name)
			}
		}
		if len(matches) == 0 {
			continue // no match — leave this output unresolved, as with an unmatched concrete glob
		}
		sort.Strings(matches)

		if outputTypeIsArray(w.def) {
			arr := make([]any, 0, len(matches))
			for _, name := range matches {
				arr = append(arr, map[string]any{
					"class":    "File",
					"location": "ws://" + resultFolder + "/" + name,
					"basename": name,
				})
			}
			outputs[w.id] = arr
			continue
		}

		if len(matches) > 1 {
			return nil, fmt.Errorf("output %q: glob %q matched %d files in %s, expected exactly one for a non-array output (declare it as an array type to accept all matches): %v",
				w.id, w.pattern, len(matches), resultFolder, matches)
		}
		outputs[w.id] = map[string]any{
			"class":    "File",
			"location": "ws://" + resultFolder + "/" + matches[0],
			"basename": matches[0],
		}
	}

	return outputs, nil
}

// listResultFolder lists a BV-BRC result folder via Workspace.ls, using the
// same per-task token as query_tasks/start_app.
func (e *BVBRCExecutor) listResultFolder(ctx context.Context, task *model.Task, resultFolder string) ([]bvbrcpkg.WorkspaceObject, error) {
	token := taskToken(task)
	if token == "" {
		return nil, fmt.Errorf("task %s: no user token for BV-BRC workspace listing", task.ID)
	}

	client := bvbrcpkg.NewClient(bvbrcpkg.Config{
		WorkspaceURL: e.workspaceURL,
		Token:        token,
		Timeout:      bvbrcpkg.DefaultTimeout,
		MaxRetries:   bvbrcpkg.DefaultMaxRetries,
		RetryDelay:   bvbrcpkg.DefaultRetryDelay,
	}, e.logger)

	result, err := client.WorkspaceLs(ctx, bvbrcpkg.WorkspaceLsInput{Paths: []string{resultFolder}})
	if err != nil {
		return nil, err
	}
	return lookupWorkspaceListing(result, resultFolder), nil
}

// lookupWorkspaceListing finds the listing for dir in a WorkspaceLs result,
// tolerating the service's trailing-slash inconsistency on the response key
// (the same tolerance internal/ui's listWorkspaceDir applies).
func lookupWorkspaceListing(result map[string][]bvbrcpkg.WorkspaceObject, dir string) []bvbrcpkg.WorkspaceObject {
	if items, ok := result[dir]; ok {
		return items
	}
	trimmed := strings.TrimSuffix(dir, "/")
	if items, ok := result[trimmed]; ok {
		return items
	}
	if items, ok := result[trimmed+"/"]; ok {
		return items
	}
	if len(result) == 1 {
		for _, items := range result {
			return items
		}
	}
	return nil
}

// outputTypeIsArray reports whether a CWL output definition declares an
// array type: "File[]", "File[]?" (optional-array shorthand), {"type":
// "array", ...}, or a union type (e.g. ["null", "File[]"]) containing one of
// those.
func outputTypeIsArray(def map[string]any) bool {
	return cwlTypeIsArray(def["type"])
}

func cwlTypeIsArray(t any) bool {
	switch v := t.(type) {
	case string:
		return strings.HasSuffix(strings.TrimSuffix(v, "?"), "[]")
	case map[string]any:
		ty, _ := v["type"].(string)
		return ty == "array"
	case []any:
		for _, item := range v {
			if cwlTypeIsArray(item) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// iterateOutputGlobs calls fn(id, glob, def) for each CWL output with a glob
// pattern, where def is that output's own definition map (outputBinding,
// type, ...).
func iterateOutputGlobs(tool map[string]any, fn func(id, glob string, def map[string]any)) {
	toolOutputs, ok := tool["outputs"]
	if !ok {
		return
	}

	visit := func(id string, m map[string]any) {
		binding, ok := m["outputBinding"].(map[string]any)
		if !ok {
			return
		}
		glob, ok := binding["glob"].(string)
		if !ok || glob == "" {
			return
		}
		fn(id, glob, m)
	}

	switch out := toolOutputs.(type) {
	case map[string]any:
		for id, def := range out {
			if m, ok := def.(map[string]any); ok {
				visit(id, m)
			}
		}
	case []any:
		for _, item := range out {
			if m, ok := item.(map[string]any); ok {
				id, _ := m["id"].(string)
				if id != "" {
					visit(id, m)
				}
			}
		}
	}
}

// matchOutputByGlob checks if a filename matches any CWL output's glob pattern.
// Returns the output ID if matched, empty string otherwise.
func matchOutputByGlob(tool map[string]any, filename, outputFile string) string {
	toolOutputs, ok := tool["outputs"]
	if !ok {
		return ""
	}

	outputMap, ok := toolOutputs.(map[string]any)
	if !ok {
		// Try as list (CWL outputs can be a list of maps with "id" field).
		if outputList, ok := toolOutputs.([]any); ok {
			for _, item := range outputList {
				if m, ok := item.(map[string]any); ok {
					id, _ := m["id"].(string)
					if id == "" || id == "result_folder" || id == "result" {
						continue
					}
					if globMatches(m, filename, outputFile) {
						return id
					}
				}
			}
		}
		return ""
	}

	for id, def := range outputMap {
		if id == "result_folder" || id == "result" {
			continue
		}
		m, ok := def.(map[string]any)
		if !ok {
			continue
		}
		if globMatches(m, filename, outputFile) {
			return id
		}
	}
	return ""
}

// globMatches checks if a filename matches the glob pattern in a CWL output definition.
func globMatches(outputDef map[string]any, filename, outputFile string) bool {
	binding, ok := outputDef["outputBinding"].(map[string]any)
	if !ok {
		return false
	}
	glob, ok := binding["glob"].(string)
	if !ok || glob == "" || glob == "." {
		return false
	}

	// Replace CWL expression $(inputs.output_file) with the actual value.
	pattern := strings.ReplaceAll(glob, "$(inputs.output_file)", outputFile)

	// Simple glob matching: support * wildcards.
	matched, _ := filepath.Match(pattern, filename)
	return matched
}

// Cancel calls AppService.kill_task for the given task.
func (e *BVBRCExecutor) Cancel(ctx context.Context, task *model.Task) error {
	if task.ExternalID == "" {
		return nil
	}

	// Get caller for this task.
	caller, _, err := e.getTaskCaller(task)
	if err != nil {
		return fmt.Errorf("task %s: no caller for cancel: %w", task.ID, err)
	}

	_, err = caller.Call(ctx, "AppService.kill_task", []any{task.ExternalID})
	if err != nil {
		return fmt.Errorf("task %s: kill_task: %w", task.ID, err)
	}
	return nil
}

// Logs calls AppService.query_app_log. On failure it falls back to stored task logs.
func (e *BVBRCExecutor) Logs(ctx context.Context, task *model.Task) (string, string, error) {
	if task.ExternalID == "" {
		return task.Stdout, task.Stderr, nil
	}

	caller, token, err := e.getTaskCaller(task)
	if err != nil {
		return task.Stdout, task.Stderr, nil
	}

	// Get stderr/stdout URLs from task details.
	result, err := caller.Call(ctx, "AppService.query_task_details", []any{task.ExternalID})
	if err != nil {
		e.logger.Debug("query_task_details failed", "task_id", task.ID, "error", err)
		return task.Stdout, task.Stderr, nil
	}

	var details []struct {
		StderrURL string `json:"stderr_url"`
		StdoutURL string `json:"stdout_url"`
	}
	if err := json.Unmarshal(result, &details); err != nil || len(details) == 0 {
		return task.Stdout, task.Stderr, nil
	}

	// Fetch logs via HTTP with OAuth auth.
	var stdout, stderr string
	if details[0].StdoutURL != "" {
		stdout = e.fetchLog(ctx, details[0].StdoutURL, token)
	}
	if details[0].StderrURL != "" {
		stderr = e.fetchLog(ctx, details[0].StderrURL, token)
	}

	if stdout == "" && stderr == "" {
		return task.Stdout, task.Stderr, nil
	}
	return stdout, stderr, nil
}

// fetchLog downloads a log file from BV-BRC using OAuth authentication.
func (e *BVBRCExecutor) fetchLog(ctx context.Context, url, token string) string {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "OAuth "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return string(body)
}

// resolveBVBRCInput converts CWL File/Directory objects to workspace path strings.
// Arrays are resolved recursively. Non-CWL values pass through unchanged.
func resolveBVBRCInput(v any) any {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			return resolveLocation(val)
		}
		// Recurse into nested maps. [bvbrc:group] / record parameters (e.g.
		// paired_end_libs) carry File/Directory objects in their fields; those
		// must be flattened to workspace path strings too, or BV-BRC receives a
		// raw CWL object ("File HASH(0x...) does not exist").
		out := make(map[string]any, len(val))
		for k, item := range val {
			out[k] = resolveBVBRCInput(item)
		}
		return out
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = resolveBVBRCInput(item)
		}
		return resolved
	default:
		return v
	}
}

// resolveLocation extracts a path from a CWL File/Directory object.
// For ws:// and file:// URIs, returns the path component.
// For shock://, returns the full URI. Falls back to path, then basename.
func resolveLocation(obj map[string]any) string {
	loc, _ := obj["location"].(string)
	if loc != "" {
		scheme, path := cwl.ParseLocationScheme(loc)
		switch scheme {
		case cwl.SchemeWorkspace, cwl.SchemeFile, "":
			return path
		case cwl.SchemeShock:
			return loc
		default:
			return path
		}
	}
	if p, ok := obj["path"].(string); ok && p != "" {
		return p
	}
	if b, ok := obj["basename"].(string); ok && b != "" {
		return b
	}
	return ""
}

// mapBVBRCState converts a BV-BRC job status string to a GoWe TaskState.
func mapBVBRCState(status string) model.TaskState {
	switch status {
	case "queued":
		return model.TaskStateQueued
	case "in-progress":
		return model.TaskStateRunning
	case "completed":
		return model.TaskStateSuccess
	case "failed", "deleted", "suspended":
		return model.TaskStateFailed
	default:
		return model.TaskStateQueued
	}
}
