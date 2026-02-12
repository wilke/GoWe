package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var taskID string

	cmd := &cobra.Command{
		Use:   "logs <submission_id>",
		Short: "View task logs for a submission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subID := args[0]

			// If no task specified, list tasks first
			if taskID == "" {
				resp, err := client.Get("/api/v1/submissions/" + subID + "/tasks/")
				if err != nil {
					return fmt.Errorf("list tasks: %w", err)
				}

				var tasks []map[string]any
				if err := json.Unmarshal(resp.Data, &tasks); err != nil {
					return fmt.Errorf("parse tasks response: %w", err)
				}

				if len(tasks) == 0 {
					fmt.Println("No tasks found.")
					return nil
				}

				// Show logs for all tasks
				for _, t := range tasks {
					tid, _ := t["id"].(string)
					stepID, _ := t["step_id"].(string)
					if err := printTaskLogs(subID, tid, stepID); err != nil {
						return err
					}
				}
				return nil
			}

			return printTaskLogs(subID, taskID, "")
		},
	}

	cmd.Flags().StringVarP(&taskID, "task", "t", "", "Specific task ID")
	return cmd
}

func printTaskLogs(subID, taskID, stepID string) error {
	resp, err := client.Get("/api/v1/submissions/" + subID + "/tasks/" + taskID + "/logs")
	if err != nil {
		return fmt.Errorf("get logs: %w", err)
	}

	var data map[string]any
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return fmt.Errorf("parse logs response: %w", err)
	}

	sid, _ := data["step_id"].(string)
	if stepID != "" {
		sid = stepID
	}

	fmt.Printf("=== %s (%s) ===\n", sid, taskID)

	if stdout, ok := data["stdout"].(string); ok && stdout != "" {
		fmt.Printf("[stdout]\n%s", stdout)
	}
	if stderr, ok := data["stderr"].(string); ok && stderr != "" {
		fmt.Printf("[stderr]\n%s", stderr)
	}

	if exitCode, ok := data["exit_code"].(float64); ok {
		fmt.Printf("[exit code: %d]\n", int(exitCode))
	}
	fmt.Println()
	return nil
}
