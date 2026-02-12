package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <submission_id>",
		Short: "Check the status of a submission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			resp, err := client.Get("/api/v1/submissions/" + id)
			if err != nil {
				return fmt.Errorf("get submission: %w", err)
			}

			var data map[string]any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			state, _ := data["state"].(string)
			wfName, _ := data["workflow_name"].(string)

			fmt.Printf("Submission: %s\n", id)
			fmt.Printf("  Workflow: %s\n", wfName)
			fmt.Printf("  State:    %s\n", state)

			if ts, ok := data["task_summary"].(map[string]any); ok {
				fmt.Printf("  Tasks:    ")
				total, _ := ts["total"].(float64)
				success, _ := ts["success"].(float64)
				running, _ := ts["running"].(float64)
				pending, _ := ts["pending"].(float64)
				failed, _ := ts["failed"].(float64)
				fmt.Printf("%d total", int(total))
				if success > 0 {
					fmt.Printf(", %d success", int(success))
				}
				if running > 0 {
					fmt.Printf(", %d running", int(running))
				}
				if pending > 0 {
					fmt.Printf(", %d pending", int(pending))
				}
				if failed > 0 {
					fmt.Printf(", %d failed", int(failed))
				}
				fmt.Println()
			}

			if tasks, ok := data["tasks"].([]any); ok && len(tasks) > 0 {
				fmt.Println("  Steps:")
				for _, t := range tasks {
					task, ok := t.(map[string]any)
					if !ok {
						continue
					}
					stepID, _ := task["step_id"].(string)
					tState, _ := task["state"].(string)
					fmt.Printf("    - %s: %s\n", stepID, tState)
				}
			}

			if createdAt, ok := data["created_at"].(string); ok {
				fmt.Printf("  Created:  %s\n", createdAt)
			}
			if completedAt, ok := data["completed_at"].(string); ok && completedAt != "" {
				fmt.Printf("  Completed: %s\n", completedAt)
			}

			return nil
		},
	}
}
