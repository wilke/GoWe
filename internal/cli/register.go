package cli

import (
	"encoding/json"
	"fmt"

	"github.com/me/gowe/internal/bundle"
	"github.com/spf13/cobra"
)

func newRegisterCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "register <tool.cwl> [tool2.cwl ...]",
		Short: "Register CWL tools and workflows with the server",
		Long:  "Bundle and register one or more CWL files with the GoWe server without creating a submission.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, cwlPath := range args {
				// 1. Bundle CWL files.
				logger.Info("bundling", "path", cwlPath)
				result, err := bundle.Bundle(cwlPath)
				if err != nil {
					return fmt.Errorf("bundle %s: %w", cwlPath, err)
				}
				logger.Debug("packed CWL document", "size", len(result.Packed), "name", result.Name)

				// 2. POST /api/v1/workflows
				wfReq := map[string]any{
					"name": result.Name,
					"cwl":  string(result.Packed),
				}
				if force {
					// Bypass server-side dedup: always create a new row
					// with a fresh parse, even if identical content was
					// already registered.
					wfReq["force"] = true
				}
				wfResp, err := client.Post("/api/v1/workflows/", wfReq)
				if err != nil {
					return fmt.Errorf("register %s: %w", cwlPath, err)
				}

				var wfData map[string]any
				if err := json.Unmarshal(wfResp.Data, &wfData); err != nil {
					return fmt.Errorf("parse response for %s: %w", cwlPath, err)
				}

				wfID, _ := wfData["id"].(string)
				wfName, _ := wfData["name"].(string)
				wfClass, _ := wfData["class"].(string)
				if wfClass == "" {
					wfClass = "Workflow"
				}
				deduplicated, _ := wfData["deduplicated"].(bool)

				if deduplicated {
					fmt.Printf("Registered (existing): %s (%s) %s\n", wfName, wfClass, wfID)
				} else {
					fmt.Printf("Registered: %s (%s) %s\n", wfName, wfClass, wfID)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Bypass dedup and always register a new row with a fresh parse")
	return cmd
}
