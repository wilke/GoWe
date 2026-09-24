package main

import (
	"fmt"
	"io"
)

// writeText renders the report as a human-readable summary. It never
// prints input values — only counts, workflow/input names, and validator
// messages (which are value-free because Run calls SubmissionInputs with
// Options{ToolLevel: true}).
func writeText(w io.Writer, r *Report) {
	fmt.Fprintf(w, "validate-replay report — %s\n", r.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(w, "database: %s\n\n", r.DBPath)

	c := r.Coverage
	fmt.Fprintf(w, "Coverage:\n")
	fmt.Fprintf(w, "  total submissions considered:     %d\n", c.Total)
	fmt.Fprintf(w, "  replayable (submitted fidelity):  %d\n", c.ReplayableSubmitted)
	fmt.Fprintf(w, "  replayable (stored fidelity):     %d\n", c.ReplayableStored)
	fmt.Fprintf(w, "  would fail under enforce:         %d\n", c.WouldFail)
	fmt.Fprintf(w, "  skipped, child submissions:       %d\n", c.Child)
	fmt.Fprintf(w, "  skipped, orphaned (no workflow):  %d\n", c.Orphaned)
	fmt.Fprintf(w, "  skipped, unparseable workflow:    %d\n", c.Unparseable)
	fmt.Fprintln(w)

	if len(r.UnparseableWorkflows) > 0 {
		fmt.Fprintf(w, "Unparseable workflows (%d):\n", len(r.UnparseableWorkflows))
		for _, u := range r.UnparseableWorkflows {
			fmt.Fprintf(w, "  - %s (%s): %d submission(s) affected\n    error: %s\n",
				u.WorkflowName, u.WorkflowID, u.AffectedSubmissions, u.Error)
		}
		fmt.Fprintln(w)
	}

	if len(r.FailureGroups) == 0 {
		fmt.Fprintln(w, "No would-be validation failures found.")
		return
	}

	fmt.Fprintf(w, "Would-be failure groups (%d), most frequent first:\n", len(r.FailureGroups))
	for _, g := range r.FailureGroups {
		fmt.Fprintf(w, "  - [%dx] workflow=%q input=%q: %s\n", g.Count, g.WorkflowName, g.InputID, g.Message)
		fmt.Fprintf(w, "         examples: %v\n", g.Examples)
	}
}
