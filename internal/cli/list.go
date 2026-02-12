package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List submissions",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Get("/api/v1/submissions/")
			if err != nil {
				return fmt.Errorf("list submissions: %w", err)
			}

			var data []map[string]any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			if len(data) == 0 {
				fmt.Println("No submissions found.")
				return nil
			}

			fmt.Printf("%-40s  %-12s  %-30s  %s\n", "ID", "STATE", "WORKFLOW", "CREATED")
			fmt.Printf("%-40s  %-12s  %-30s  %s\n", "----", "-----", "--------", "-------")
			for _, sub := range data {
				id, _ := sub["id"].(string)
				state, _ := sub["state"].(string)
				wfName, _ := sub["workflow_name"].(string)
				createdAt, _ := sub["created_at"].(string)
				fmt.Printf("%-40s  %-12s  %-30s  %s\n", id, state, wfName, createdAt)
			}

			if resp.Pagination != nil && resp.Pagination.HasMore {
				fmt.Printf("\n(%d of %d shown)\n", len(data), resp.Pagination.Total)
			}

			return nil
		},
	}
}
