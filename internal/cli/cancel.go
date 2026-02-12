package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <submission_id>",
		Short: "Cancel a running submission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			resp, err := client.Put("/api/v1/submissions/"+id+"/cancel", nil)
			if err != nil {
				return fmt.Errorf("cancel submission: %w", err)
			}

			var data map[string]any
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				return fmt.Errorf("parse response: %w", err)
			}

			state, _ := data["state"].(string)
			cancelled, _ := data["tasks_cancelled"].(float64)
			completed, _ := data["tasks_already_completed"].(float64)

			fmt.Printf("Submission %s: %s\n", id, state)
			fmt.Printf("  Tasks cancelled: %d\n", int(cancelled))
			fmt.Printf("  Tasks already completed: %d\n", int(completed))
			return nil
		},
	}
}
