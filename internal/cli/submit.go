package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/me/gowe/internal/bundle"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newSubmitCmd() *cobra.Command {
	var inputsFile string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "submit <workflow.cwl>",
		Short: "Submit a CWL workflow for execution",
		Long:  "Bundle a CWL workflow and its tool references, then submit to the GoWe server.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowPath := args[0]

			// 1. Bundle CWL files
			logger.Info("bundling workflow", "path", workflowPath)
			result, err := bundle.Bundle(workflowPath)
			if err != nil {
				return fmt.Errorf("bundle: %w", err)
			}
			logger.Debug("packed CWL document", "size", len(result.Packed), "name", result.Name)
			logger.Debug("packed content", "cwl", string(result.Packed))

			// 2. Read inputs if provided
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
			}

			// 3. POST /api/v1/workflows
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
			fmt.Printf("Workflow registered: %s\n", workflowID)

			// 4. POST /api/v1/submissions
			subReq := map[string]any{
				"workflow_id": workflowID,
				"inputs":      inputs,
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
	return cmd
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
			fmt.Printf("    %d. %s", i+1, id)
			if appID != "" {
				fmt.Printf(" -> %s (%s)", appID, execType)
			}
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

	if errs, ok := data["errors"].([]any); ok && len(errs) > 0 {
		fmt.Println("  Errors:")
		for _, e := range errs {
			if em, ok := e.(map[string]any); ok {
				fmt.Printf("    - %s: %s\n", em["path"], em["message"])
			}
		}
	}

	if warns, ok := data["warnings"].([]any); ok && len(warns) > 0 {
		fmt.Println("  Warnings:")
		for _, w := range warns {
			if wm, ok := w.(map[string]any); ok {
				fmt.Printf("    - %s: %s\n", wm["path"], wm["message"])
			}
		}
	}

	fmt.Println("\nNo submission created. Use without --dry-run to execute.")
	return nil
}
