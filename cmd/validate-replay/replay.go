// Package main implements validate-replay, a read-only report tool that
// replays stored submissions against internal/validate's input type
// validation to estimate how many would fail once enforce mode is turned
// on. It never mutates the database and never prints input values (secret
// or otherwise) — see internal/validate.Options.ToolLevel.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/me/gowe/internal/validate"
)

// workflowRow is the subset of the workflows table this tool needs.
// RawCWL and SecretInputs are read solely to feed validate.SubmissionInputs;
// they are never echoed to the report (only the aggregate outcome is).
type workflowRow struct {
	ID           string
	Name         string
	RawCWL       string
	SecretInputs []string
}

// submissionRow is the subset of the submissions table this tool needs.
// Note what is deliberately NOT read: secrets, secret_names, user_token,
// token_expiry, auth_provider. Those columns are never selected by the
// queries in this file.
type submissionRow struct {
	ID              string
	WorkflowID      string
	ParentTaskID    string
	SubmittedInputs map[string]any
	Inputs          map[string]any
	CreatedAt       time.Time
}

// RunOptions controls which submissions Run considers.
type RunOptions struct {
	// Limit caps the number of submissions processed, in created_at
	// ascending order. Zero means "all".
	Limit int
	// Since, if non-nil, excludes submissions created strictly before this
	// time.
	Since *time.Time
}

// Coverage summarises how the analyzed submission set breaks down.
type Coverage struct {
	Total               int `json:"total"`
	ReplayableSubmitted int `json:"replayable_submitted_fidelity"`
	ReplayableStored    int `json:"replayable_stored_fidelity"`
	WouldFail           int `json:"would_fail"`
	Child               int `json:"child"`
	Orphaned            int `json:"orphaned"`
	Unparseable         int `json:"unparseable"`
}

// UnparseableWorkflow records a workflow whose raw_cwl failed to re-parse,
// reported once regardless of how many submissions reference it.
type UnparseableWorkflow struct {
	WorkflowID          string `json:"workflow_id"`
	WorkflowName        string `json:"workflow_name"`
	Error               string `json:"error"`
	AffectedSubmissions int    `json:"affected_submissions"`
}

// FailureGroup is one (workflow, input, message) bucket of would-be
// validation failures. Message is always value-free (ToolLevel: true).
type FailureGroup struct {
	WorkflowName string   `json:"workflow_name"`
	InputID      string   `json:"input_id"`
	Message      string   `json:"message"`
	Count        int      `json:"count"`
	Examples     []string `json:"example_submission_ids"`
}

// Report is the full output of a replay run.
type Report struct {
	GeneratedAt          time.Time             `json:"generated_at"`
	DBPath               string                `json:"db_path"`
	Coverage             Coverage              `json:"coverage"`
	UnparseableWorkflows []UnparseableWorkflow `json:"unparseable_workflows,omitempty"`
	FailureGroups        []FailureGroup        `json:"failure_groups,omitempty"`
}

// Run loads workflows and submissions from db (already opened by the
// caller, read-only) and replays each non-child, non-orphaned submission's
// inputs through validate.SubmissionInputs. It performs no writes.
func Run(ctx context.Context, db *sql.DB, dbPath string, opts RunOptions) (*Report, error) {
	workflows, err := loadWorkflows(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("load workflows: %w", err)
	}
	submissions, err := loadSubmissions(ctx, db, opts)
	if err != nil {
		return nil, fmt.Errorf("load submissions: %w", err)
	}

	report := &Report{GeneratedAt: time.Now().UTC(), DBPath: dbPath}

	unparseable := map[string]*UnparseableWorkflow{}
	type groupKey struct {
		workflow string
		input    string
		message  string
	}
	groups := map[groupKey]*FailureGroup{}

	for _, sub := range submissions {
		report.Coverage.Total++

		if sub.ParentTaskID != "" {
			report.Coverage.Child++
			continue
		}
		wf, ok := workflows[sub.WorkflowID]
		if !ok {
			report.Coverage.Orphaned++
			continue
		}

		inputs := sub.Inputs
		fidelity := "stored"
		if len(sub.SubmittedInputs) > 0 {
			inputs = sub.SubmittedInputs
			fidelity = "submitted"
		}

		fieldErrs, verr := validate.SubmissionInputs([]byte(wf.RawCWL), wf.SecretInputs, inputs, validate.Options{ToolLevel: true})
		if verr != nil {
			report.Coverage.Unparseable++
			u, seen := unparseable[wf.ID]
			if !seen {
				u = &UnparseableWorkflow{WorkflowID: wf.ID, WorkflowName: wf.Name, Error: verr.Error()}
				unparseable[wf.ID] = u
			}
			u.AffectedSubmissions++
			continue
		}

		if fidelity == "submitted" {
			report.Coverage.ReplayableSubmitted++
		} else {
			report.Coverage.ReplayableStored++
		}

		if len(fieldErrs) == 0 {
			continue
		}
		report.Coverage.WouldFail++

		for _, fe := range fieldErrs {
			key := groupKey{workflow: wf.Name, input: fe.Field, message: fe.Message}
			g, seen := groups[key]
			if !seen {
				g = &FailureGroup{WorkflowName: wf.Name, InputID: fe.Field, Message: fe.Message}
				groups[key] = g
			}
			g.Count++
			if len(g.Examples) < 3 {
				g.Examples = append(g.Examples, sub.ID)
			}
		}
	}

	for _, u := range unparseable {
		report.UnparseableWorkflows = append(report.UnparseableWorkflows, *u)
	}
	sort.Slice(report.UnparseableWorkflows, func(i, j int) bool {
		return report.UnparseableWorkflows[i].WorkflowName < report.UnparseableWorkflows[j].WorkflowName
	})

	for _, g := range groups {
		report.FailureGroups = append(report.FailureGroups, *g)
	}
	sort.Slice(report.FailureGroups, func(i, j int) bool {
		a, b := report.FailureGroups[i], report.FailureGroups[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.WorkflowName != b.WorkflowName {
			return a.WorkflowName < b.WorkflowName
		}
		if a.InputID != b.InputID {
			return a.InputID < b.InputID
		}
		return a.Message < b.Message
	})

	return report, nil
}

// loadWorkflows reads every workflow's id, name, raw_cwl, secret_inputs.
// It never selects any other column.
func loadWorkflows(ctx context.Context, db *sql.DB) (map[string]*workflowRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, raw_cwl, secret_inputs FROM workflows`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]*workflowRow{}
	for rows.Next() {
		var wf workflowRow
		var secretInputsCol *string
		if err := rows.Scan(&wf.ID, &wf.Name, &wf.RawCWL, &secretInputsCol); err != nil {
			return nil, err
		}
		if secretInputsCol != nil && *secretInputsCol != "" {
			if err := json.Unmarshal([]byte(*secretInputsCol), &wf.SecretInputs); err != nil {
				return nil, fmt.Errorf("unmarshal secret_inputs for workflow %s: %w", wf.ID, err)
			}
		}
		out[wf.ID] = &wf
	}
	return out, rows.Err()
}

// loadSubmissions reads id, workflow_id, parent_task_id, submitted_inputs,
// inputs, created_at for every submission (subject to opts), ordered by
// created_at ascending. It never selects secrets, secret_names, user_token,
// token_expiry, or auth_provider.
func loadSubmissions(ctx context.Context, db *sql.DB, opts RunOptions) ([]submissionRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, workflow_id, parent_task_id, submitted_inputs, inputs, created_at
		 FROM submissions ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []submissionRow
	for rows.Next() {
		var sub submissionRow
		var submittedInputsCol *string
		var inputsJSON, createdAt string
		if err := rows.Scan(&sub.ID, &sub.WorkflowID, &sub.ParentTaskID, &submittedInputsCol, &inputsJSON, &createdAt); err != nil {
			return nil, err
		}

		if inputsJSON != "" {
			if err := json.Unmarshal([]byte(inputsJSON), &sub.Inputs); err != nil {
				return nil, fmt.Errorf("unmarshal inputs for submission %s: %w", sub.ID, err)
			}
		}
		if submittedInputsCol != nil && *submittedInputsCol != "" {
			if err := json.Unmarshal([]byte(*submittedInputsCol), &sub.SubmittedInputs); err != nil {
				return nil, fmt.Errorf("unmarshal submitted_inputs for submission %s: %w", sub.ID, err)
			}
		}
		if createdAt != "" {
			t, err := time.Parse(time.RFC3339Nano, createdAt)
			if err != nil {
				return nil, fmt.Errorf("corrupt created_at %q for submission %s: %w", createdAt, sub.ID, err)
			}
			sub.CreatedAt = t
		}

		if opts.Since != nil && sub.CreatedAt.Before(*opts.Since) {
			continue
		}
		out = append(out, sub)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out, rows.Err()
}
