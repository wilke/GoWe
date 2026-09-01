package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// The timing* structs mirror the GET /api/v1/submissions/{id}/timing payload
// (see internal/server/handler_timing.go).
type timingSubmissionRow struct {
	ID            string   `json:"id"`
	State         string   `json:"state"`
	WallS         *float64 `json:"wall_s"`
	SchedulingS   *float64 `json:"scheduling_s"`
	ComputeS      float64  `json:"compute_s"`
	QueueS        float64  `json:"queue_s"`
	PrestageS     *float64 `json:"prestage_s"`
	PoststageS    *float64 `json:"poststage_s"`
	CriticalPathS *float64 `json:"critical_path_s"`
}

type timingStepRow struct {
	StepID  string   `json:"step_id"`
	State   string   `json:"state"`
	WallS   *float64 `json:"wall_s"`
	FanInS  *float64 `json:"fan_in_s"`
	MaxRunS *float64 `json:"max_run_s"`
	Tasks   int      `json:"tasks"`
	Inline  bool     `json:"inline"`
}

type timingTaskRow struct {
	TaskID        string   `json:"task_id"`
	StepID        string   `json:"step_id"`
	ScatterIndex  int      `json:"scatter_index"`
	Executor      string   `json:"executor"`
	WorkerGroup   string   `json:"worker_group"`
	State         string   `json:"state"`
	Kind          string   `json:"kind"`
	QueueS        *float64 `json:"queue_s"`
	DispatchS     *float64 `json:"dispatch_s"`
	CheckoutWaitS *float64 `json:"checkout_wait_s"`
	StageInS      *float64 `json:"stage_in_s"`
	ComputeS      *float64 `json:"compute_s"`
	StageOutS     *float64 `json:"stage_out_s"`
	RunS          *float64 `json:"run_s"`
	Retrying      bool     `json:"retrying"`
	RetryCount    int      `json:"retry_count"`
}

type timingBody struct {
	Submission timingSubmissionRow `json:"submission"`
	Steps      []timingStepRow     `json:"steps"`
	Tasks      []timingTaskRow     `json:"tasks"`
	Children   []timingBody        `json:"children"`
}

func newStatusCmd() *cobra.Command {
	var (
		showTiming bool
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "status <submission_id>",
		Short: "Check the status of a submission",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if asJSON && !showTiming {
				return errors.New("--json requires --timing")
			}
			if showTiming {
				return runTimingStatus(cmd, id, asJSON)
			}

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
	cmd.Flags().BoolVar(&showTiming, "timing", false, "Show the timing breakdown (steps, tasks, queue/run durations)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw timing JSON (requires --timing)")
	return cmd
}

// runTimingStatus fetches and renders the /timing view of a submission.
func runTimingStatus(cmd *cobra.Command, id string, asJSON bool) error {
	resp, err := client.Get("/api/v1/submissions/" + id + "/timing?include_children=true")
	if err != nil {
		return fmt.Errorf("get submission timing: %w", err)
	}

	out := cmd.OutOrStdout()
	if asJSON {
		var buf bytes.Buffer
		if err := json.Indent(&buf, resp.Data, "", "  "); err != nil {
			return fmt.Errorf("format response: %w", err)
		}
		buf.WriteByte('\n')
		_, err = out.Write(buf.Bytes())
		return err
	}

	var body timingBody
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	printTimingReport(out, &body)
	return nil
}

// printTimingReport renders one submission's timing (and recursively its
// sub-workflow children) as submission → steps → tasks tables.
func printTimingReport(w io.Writer, r *timingBody) {
	s := r.Submission
	fmt.Fprintf(w, "Submission %s [%s]  wall=%s scheduling=%s compute=%s queue=%s prestage=%s poststage=%s critical-path=%s\n",
		s.ID, s.State,
		fmtSeconds(s.WallS), fmtSeconds(s.SchedulingS),
		fmtSecondsVal(s.ComputeS), fmtSecondsVal(s.QueueS),
		fmtSeconds(s.PrestageS), fmtSeconds(s.PoststageS),
		fmtSeconds(s.CriticalPathS))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  STEP\tSTATE\tWALL\tFAN-IN\tMAX-RUN\tTASKS")
	for _, st := range r.Steps {
		name := st.StepID
		if st.Inline {
			name += " (inline)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%d\n",
			name, st.State, fmtSeconds(st.WallS), fmtSeconds(st.FanInS), fmtSeconds(st.MaxRunS), st.Tasks)
	}
	tw.Flush()

	// The DISPATCH/CHECKOUT/STAGE-IN/COMPUTE/STAGE-OUT columns are the #184
	// PR2 breakdown (submit→dispatch→checkout→stage-in→compute→stage-out);
	// they render "-" for executors/rows that don't carry that data (only
	// worker tasks report stage timings; CHECKOUT is near-zero for sync
	// executors).
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  TASK\tSTEP\tIDX\tEXECUTOR\tSTATE\tKIND\tQUEUE\tDISPATCH\tCHECKOUT\tSTAGE-IN\tCOMPUTE\tSTAGE-OUT\tRUN\tRETRIES")
	for _, t := range r.Tasks {
		idx := "-"
		if t.ScatterIndex >= 0 {
			idx = fmt.Sprint(t.ScatterIndex)
		}
		state := t.State
		if t.Retrying {
			state += " (retrying)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			t.TaskID, t.StepID, idx, t.Executor, state, t.Kind,
			fmtSeconds(t.QueueS), fmtSeconds(t.DispatchS), fmtSeconds(t.CheckoutWaitS),
			fmtSeconds(t.StageInS), fmtSeconds(t.ComputeS), fmtSeconds(t.StageOutS),
			fmtSeconds(t.RunS), t.RetryCount)
	}
	tw.Flush()

	for i := range r.Children {
		fmt.Fprintln(w)
		printTimingReport(w, &r.Children[i])
	}
}

// fmtSeconds renders an optional seconds value; absent values print "-".
func fmtSeconds(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmtSecondsVal(*v)
}

func fmtSecondsVal(v float64) string {
	return fmt.Sprintf("%.1fs", v)
}
