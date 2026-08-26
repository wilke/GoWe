package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/me/gowe/internal/bundle"
	"github.com/me/gowe/internal/bvbrc"
	bvbrcpkg "github.com/me/gowe/pkg/bvbrc"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newSubmitCmd() *cobra.Command {
	var inputsFile string
	var dryRun bool
	var noUpload bool
	var workspaceUpload bool
	var workerGroup string
	var outputDest string
	var debug bool
	var workflowRef string
	var workspaceURL string

	cmd := &cobra.Command{
		Use:   "submit [<workflow.cwl>]",
		Short: "Submit a CWL workflow for execution",
		Long: `Bundle a CWL workflow and its tool references, then submit to the GoWe server.

Alternatively, use --workflow to reference an already-registered workflow by ID or name:
  gowe submit --workflow wf_c3a3f50c-... -i job.json
  gowe submit --workflow protein-structure-prediction -i job.json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && workflowRef == "" {
				return fmt.Errorf("either a CWL file path or --workflow is required")
			}
			if len(args) > 0 && workflowRef != "" {
				return fmt.Errorf("specify either a CWL file path or --workflow, not both")
			}

			var workflowID string

			if workflowRef != "" {
				// Look up existing workflow by ID or name.
				path := "/api/v1/workflows/" + workflowRef
				resp, err := client.Get(path)
				if err != nil {
					return fmt.Errorf("lookup workflow %q: %w", workflowRef, err)
				}
				var wfData map[string]any
				if err := json.Unmarshal(resp.Data, &wfData); err != nil {
					return fmt.Errorf("parse workflow response: %w", err)
				}
				id, ok := wfData["id"].(string)
				if !ok {
					return fmt.Errorf("workflow response missing 'id' field")
				}
				workflowID = id
				name, _ := wfData["name"].(string)
				fmt.Printf("Using workflow: %s (%s)\n", name, workflowID)
			} else {
				workflowPath := args[0]

				// 1. Bundle CWL files
				logger.Info("bundling workflow", "path", workflowPath)
				result, err := bundle.Bundle(workflowPath)
				if err != nil {
					return fmt.Errorf("bundle: %w", err)
				}
				logger.Debug("packed CWL document", "size", len(result.Packed), "name", result.Name)
				logger.Debug("packed content", "cwl", string(result.Packed))

				// 2. POST /api/v1/workflows
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
				id, ok := wfData["id"].(string)
				if !ok {
					return fmt.Errorf("workflow response missing 'id' field")
				}
				workflowID = id
				fmt.Printf("Workflow registered: %s\n", workflowID)
			}

			// Read inputs if provided.
			var inputs map[string]any
			if inputsFile != "" {
				data, err := os.ReadFile(inputsFile)
				if err != nil {
					return fmt.Errorf("read inputs: %w", err)
				}
				if err := yaml.Unmarshal(data, &inputs); err != nil {
					return fmt.Errorf("parse inputs: %w", err)
				}
				logger.Debug("parsed inputs", "count", len(inputs))

				// Resolve File/Directory paths relative to job file location.
				jobDir, err := filepath.Abs(filepath.Dir(inputsFile))
				if err != nil {
					return fmt.Errorf("get inputs directory: %w", err)
				}
				if resolved, ok := bundle.ResolveFilePaths(inputs, jobDir).(map[string]any); ok {
					inputs = resolved
				}

				// Upload File/Directory inputs unless --no-upload.
				if !noUpload {
					if workspaceUpload {
						// Upload to BV-BRC workspace.
						uploaded, err := uploadInputsToWorkspace(inputs, client.Token, workspaceURL)
						if err != nil {
							return fmt.Errorf("workspace upload: %w", err)
						}
						inputs = uploaded
					} else {
						// Upload to server file storage.
						uploaded, err := uploadInputFiles(inputs, false)
						if err != nil {
							return fmt.Errorf("upload inputs: %w", err)
						}
						inputs = uploaded
					}
				}
			}

			// 4. POST /api/v1/submissions
			labels := map[string]string{}
			if workerGroup != "" {
				labels["worker_group"] = workerGroup
			}
			if debug {
				labels["debug"] = "true"
			}
			subReq := map[string]any{
				"workflow_id": workflowID,
				"inputs":      inputs,
				"labels":      labels,
			}
			if outputDest != "" {
				subReq["output_destination"] = outputDest
			}

			subPath := "/api/v1/submissions/"
			if dryRun {
				subPath += "?dry_run=true"
			}

			subResp, err := client.Post(subPath, subReq)
			if err != nil {
				return fmt.Errorf("create submission: %w", err)
			}

			var subData map[string]any
			if err := json.Unmarshal(subResp.Data, &subData); err != nil {
				return fmt.Errorf("parse submission response: %w", err)
			}

			if dryRun {
				return printDryRunReport(subData)
			}

			submissionID, ok := subData["id"].(string)
			if !ok {
				return fmt.Errorf("submission response missing 'id' field")
			}
			state, _ := subData["state"].(string)
			fmt.Printf("Submission created: %s (state: %s)\n", submissionID, state)
			return nil
		},
	}

	cmd.Flags().StringVarP(&inputsFile, "inputs", "i", "", "Input values file (YAML/JSON)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate without executing")
	cmd.Flags().BoolVar(&noUpload, "no-upload", false, "Disable file upload; assume files are accessible on workers")
	cmd.Flags().BoolVar(&workspaceUpload, "workspace-upload", false, "Upload input files to BV-BRC workspace instead of server (for bvbrc executor)")
	cmd.Flags().StringVar(&workerGroup, "group", "", "Target worker group for task scheduling")
	cmd.Flags().BoolVar(&debug, "debug", false, "Debug mode: workers keep all task working data for this submission (no cleanup)")
	cmd.Flags().StringVar(&outputDest, "output-destination", "", "Target URI for uploading outputs (e.g., ws:///user@bvbrc/home/results/)")
	cmd.Flags().StringVar(&workflowRef, "workflow", "", "Submit using an already-registered workflow (by ID or name)")
	cmd.Flags().StringVar(&workspaceURL, "workspace-url", defaultWorkspaceURL(), "BV-BRC Workspace service URL for --workspace-upload (or GOWE_WORKSPACE_URL env)")
	return cmd
}

// defaultWorkspaceURL returns the Workspace service URL, checking GOWE_WORKSPACE_URL first.
func defaultWorkspaceURL() string {
	if u := os.Getenv("GOWE_WORKSPACE_URL"); u != "" {
		return u
	}
	return bvbrcpkg.DefaultWorkspaceURL
}

// Workspace upload settings for the CLI: the JSON-RPC timeout (file PUTs use
// the client's dedicated upload transport and are bounded by ctx only), the
// JSON-RPC retry budget, and the number of end-to-end upload attempts per file.
const (
	workspaceRPCTimeout    = 60 * time.Second
	workspaceRPCRetries    = 3
	workspaceUploadRetries = 3
)

// uploadInputsToWorkspace uploads local File inputs to the BV-BRC workspace.
// Files are uploaded to /user/home/.gowe-inputs/<basename> and the input
// locations are rewritten to ws:// URIs.
func uploadInputsToWorkspace(inputs map[string]any, token, workspaceURL string) (map[string]any, error) {
	if token == "" {
		return nil, fmt.Errorf("BV-BRC token required for workspace upload (run 'gowe login')")
	}

	tokenInfo := bvbrc.ParseToken(token)
	if tokenInfo.Username == "" {
		return nil, fmt.Errorf("cannot parse username from token")
	}
	if workspaceURL == "" {
		workspaceURL = bvbrcpkg.DefaultWorkspaceURL
	}

	wsLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	wsClient := bvbrcpkg.NewClient(bvbrcpkg.Config{
		WorkspaceURL: workspaceURL,
		Token:        token,
		Timeout:      workspaceRPCTimeout,
		MaxRetries:   workspaceRPCRetries,
		RetryDelay:   bvbrcpkg.DefaultRetryDelay,
	}, wsLogger)

	destFolder := "/" + tokenInfo.Username + "/home/.gowe-inputs"

	// Ctrl-C / SIGTERM cancel the uploads through this context rather than
	// killing the process outright, so an interrupted upload still gets to
	// delete its placeholder (that cleanup detaches from the cancellation and
	// bounds itself).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Ensure destination folder exists. An existing folder is expected; any
	// other failure surfaces again as an upload error, so log and continue.
	if _, err := wsClient.WorkspaceCreateFolder(ctx, destFolder); err != nil {
		if bvbrcpkg.IsExistsError(err) {
			wsLogger.Debug("workspace folder exists", "path", destFolder)
		} else {
			wsLogger.Warn("workspace folder create failed", "path", destFolder, "error", err)
		}
	}

	result := make(map[string]any)
	for k, v := range inputs {
		uploaded, err := uploadWorkspaceValue(ctx, wsClient, v, destFolder)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", k, err)
		}
		result[k] = uploaded
	}
	return result, nil
}

func uploadWorkspaceValue(ctx context.Context, ws *bvbrcpkg.Client, val any, destFolder string) (any, error) {
	switch v := val.(type) {
	case map[string]any:
		class, _ := v["class"].(string)
		if class == "File" {
			return uploadFileToWorkspace(ctx, ws, v, destFolder)
		}
		// Recurse into nested maps.
		result := make(map[string]any)
		for k, inner := range v {
			uploaded, err := uploadWorkspaceValue(ctx, ws, inner, destFolder)
			if err != nil {
				return nil, err
			}
			result[k] = uploaded
		}
		return result, nil
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			uploaded, err := uploadWorkspaceValue(ctx, ws, item, destFolder)
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

func uploadFileToWorkspace(ctx context.Context, ws *bvbrcpkg.Client, fileObj map[string]any, destFolder string) (map[string]any, error) {
	localPath := fileLocationToPath(fileObj)
	if localPath == "" {
		return fileObj, nil
	}

	// Skip if already a ws:// path.
	if loc, _ := fileObj["location"].(string); len(loc) > 5 && loc[:5] == "ws://" {
		return fileObj, nil
	}

	// Check file exists locally.
	if _, err := os.Stat(localPath); err != nil {
		return fileObj, nil
	}

	basename := filepath.Base(localPath)
	wsPath := destFolder + "/" + basename
	fmt.Fprintf(os.Stderr, "  Uploading %s → ws://%s\n", basename, wsPath)

	if err := uploadLocalFile(ctx, ws, localPath, wsPath); err != nil {
		return nil, fmt.Errorf("workspace upload %s: %w", basename, err)
	}

	result := make(map[string]any)
	for k, v := range fileObj {
		result[k] = v
	}
	result["location"] = "ws://" + wsPath
	result["path"] = wsPath
	result["basename"] = basename
	return result, nil
}

// uploadLocalFile streams localPath into the workspace object at wsPath,
// re-opening the file for every attempt: an attempt verifies what the service
// stored and cleans up after itself, so a retry always starts afresh.
func uploadLocalFile(ctx context.Context, ws *bvbrcpkg.Client, localPath, wsPath string) error {
	var lastErr error
	for attempt := 0; attempt < workspaceUploadRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		err := func() error {
			f, err := os.Open(localPath)
			if err != nil {
				return fmt.Errorf("open %s: %w", localPath, err)
			}
			defer f.Close()

			st, err := f.Stat()
			if err != nil {
				return fmt.Errorf("stat %s: %w", localPath, err)
			}

			_, err = ws.WorkspaceUploadReader(ctx, wsPath, filepath.Base(localPath), f, st.Size(), bvbrcpkg.WorkspaceTypeUnspecified)
			return err
		}()
		if err == nil {
			return nil
		}
		lastErr = err
		fmt.Fprintf(os.Stderr, "  upload attempt %d failed: %v\n", attempt+1, err)
	}

	return fmt.Errorf("after %d attempts: %w", workspaceUploadRetries, lastErr)
}

func printDryRunReport(data map[string]any) error {
	valid, _ := data["valid"].(bool)

	wfName := ""
	if wf, ok := data["workflow"].(map[string]any); ok {
		wfName, _ = wf["name"].(string)
	}

	fmt.Printf("Dry-run: %s\n", wfName)

	if valid {
		fmt.Println("  Workflow: valid")
	} else {
		fmt.Println("  Workflow: INVALID")
	}

	if steps, ok := data["steps"].([]any); ok {
		fmt.Printf("  Steps:\n")
		for i, s := range steps {
			step, ok := s.(map[string]any)
			if !ok {
				continue
			}
			id, _ := step["id"].(string)
			execType, _ := step["executor_type"].(string)
			appID, _ := step["bvbrc_app_id"].(string)
			available, _ := step["executor_available"].(bool)
			fmt.Printf("    %d. %s", i+1, id)
			if appID != "" {
				fmt.Printf(" -> %s", appID)
			}
			fmt.Printf(" (%s", execType)
			if !available {
				fmt.Printf(", unavailable")
			}
			fmt.Printf(")")
			fmt.Println()
		}
	}

	if dagOK, ok := data["dag_acyclic"].(bool); ok {
		if dagOK {
			fmt.Println("  DAG: acyclic")
		} else {
			fmt.Println("  DAG: CYCLIC (error)")
		}
	}

	if inputsValid, ok := data["inputs_valid"].(bool); ok && !inputsValid {
		fmt.Println("  Inputs: INVALID")
	}

	if execAvail, ok := data["executor_availability"].(map[string]any); ok && len(execAvail) > 0 {
		fmt.Printf("  Executors:\n")
		for name, status := range execAvail {
			fmt.Printf("    %s: %s\n", name, status)
		}
	}

	if errs, ok := data["errors"].([]any); ok && len(errs) > 0 {
		fmt.Println("  Errors:")
		for _, e := range errs {
			if em, ok := e.(map[string]any); ok {
				field, _ := em["field"].(string)
				msg, _ := em["message"].(string)
				fmt.Printf("    - %s: %s\n", field, msg)
			}
		}
	}

	if warns, ok := data["warnings"].([]any); ok && len(warns) > 0 {
		fmt.Println("  Warnings:")
		for _, w := range warns {
			if wm, ok := w.(map[string]any); ok {
				field, _ := wm["field"].(string)
				msg, _ := wm["message"].(string)
				fmt.Printf("    - %s: %s\n", field, msg)
			}
		}
	}

	fmt.Println("\nNo submission created. Use without --dry-run to execute.")
	return nil
}
