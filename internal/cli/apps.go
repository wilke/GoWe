package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newAppsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "List available BV-BRC applications",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Get("/api/v1/apps/")
			if err != nil {
				return fmt.Errorf("list apps: %w", err)
			}

			var data []map[string]any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			if len(data) == 0 {
				fmt.Println("No apps found.")
				return nil
			}

			fmt.Printf("%-35s  %s\n", "APP ID", "DESCRIPTION")
			fmt.Printf("%-35s  %s\n", "------", "-----------")
			for _, app := range data {
				id, _ := app["id"].(string)
				desc, _ := app["description"].(string)
				fmt.Printf("%-35s  %s\n", id, desc)
			}
			return nil
		},
	}
	return cmd
}
