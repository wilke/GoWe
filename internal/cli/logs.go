package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/me/gowe/pkg/model"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var taskID string
	var attempts bool

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
				showAttempts := attempts
				for _, t := range tasks {
					tid, _ := t["id"].(string)
					stepID, _ := t["step_id"].(string)
					if err := printTaskLogs(subID, tid, stepID); err != nil {
						return err
					}
					if showAttempts {
						available, err := printTaskAttempts(subID, tid)
						if err != nil {
							return err
						}
						if !available {
							// Said once; the other tasks' logs still follow.
							fmt.Fprintln(cmd.ErrOrStderr(), noAttemptHistory)
							showAttempts = false
						}
					}
				}
				return nil
			}

			if err := printTaskLogs(subID, taskID, ""); err != nil {
				return err
			}
			if attempts {
				available, err := printTaskAttempts(subID, taskID)
				if err == nil && !available {
					fmt.Fprintln(cmd.ErrOrStderr(), noAttemptHistory)
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&taskID, "task", "t", "", "Specific task ID")
	cmd.Flags().BoolVar(&attempts, "attempts", false, "Also print the earlier attempts of each task (exit code, worker, times, stderr)")
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

// noAttemptHistory is the notice for a server without the attempts route.
const noAttemptHistory = "attempt history is not available on this server"

// printTaskAttempts prints the earlier attempts of a task from
// GET /api/v1/submissions/{id}/tasks/{tid}/attempts. It returns false and no
// error when the server answers 404 (a server older than the route).
func printTaskAttempts(subID, taskID string) (bool, error) {
	resp, err := client.Get("/api/v1/submissions/" + subID + "/tasks/" + taskID + "/attempts")
	if err != nil {
		if httpStatus(err) == http.StatusNotFound {
			return false, nil
		}
		return true, fmt.Errorf("get attempts: %w", err)
	}
	var data struct {
		Attempts []model.TaskAttempt `json:"attempts"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return true, fmt.Errorf("parse attempts response: %w", err)
	}
	writeTaskAttempts(os.Stdout, taskID, data.Attempts)
	return true, nil
}

// writeTaskAttempts writes the attempts of task taskID to w: one header line
// per attempt (attempt, reason, exit code, worker, times), then its stderr.
func writeTaskAttempts(w io.Writer, taskID string, attempts []model.TaskAttempt) {
	if len(attempts) == 0 {
		fmt.Fprintf(w, "--- %s: no earlier attempts ---\n\n", taskID)
		return
	}
	for _, a := range attempts {
		exit := "-"
		if a.ExitCode != nil {
			exit = strconv.Itoa(*a.ExitCode)
		}
		fmt.Fprintf(w, "--- %s attempt %d: reason %s, exit %s, worker %s, %s to %s ---\n",
			taskID, a.Attempt, a.Reason, exit, a.WorkerLabel(), attemptTimeString(a.StartedAt), attemptTimeString(a.CompletedAt))
		if a.Stderr != "" {
			fmt.Fprintf(w, "[stderr]\n%s", a.Stderr)
			if a.Stderr[len(a.Stderr)-1] != '\n' {
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintln(w)
	}
}

// attemptTimeString formats an attempt time in RFC 3339, "-" when unknown.
func attemptTimeString(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}
