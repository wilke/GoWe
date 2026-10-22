package main

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// enumWorkflowCWL declares a single enum input "mode" with symbols
// fast|slow, plus a required string "password" (used as the secret input
// in some test cases). It mirrors the fixture used by
// internal/validate.TestSubmissionInputs.
const enumWorkflowCWL = `cwlVersion: v1.2
class: Workflow
inputs:
  mode:
    type:
      type: enum
      symbols: [fast, slow]
  password: string
outputs: []
steps: []
`

// newTestDB creates a temp-file SQLite database with the real GoWe schema
// (via internal/store, so migrations run exactly as production) and
// returns its path. The store is closed before returning so nothing else
// holds the file open.
func newTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "replay-test.db")
	st, err := store.NewSQLiteStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return dbPath
}

// rawExec opens its own read-write connection to run setup SQL the store's
// typed API doesn't cover (e.g. forcing submitted_inputs back to empty to
// simulate the "stored fidelity" fallback), then closes it.
func rawExec(t *testing.T, dbPath, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open for raw exec: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("raw exec %q: %v", query, err)
	}
}

func openTestDBReadOnly(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := openReadOnly(dbPath)
	if err != nil {
		t.Fatalf("openReadOnly: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRun(t *testing.T) {
	dbPath := newTestDB(t)
	st, err := store.NewSQLiteStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	ctx := context.Background()

	now := time.Now().UTC()

	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf-good", Name: "enum-flow", CWLVersion: "v1.2", Class: "Workflow",
		RawCWL: enumWorkflowCWL, CreatedAt: now, UpdatedAt: now,
		SecretInputs: []string{"password"},
	}); err != nil {
		t.Fatalf("create workflow wf-good: %v", err)
	}
	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf-bad-cwl", Name: "broken-flow", CWLVersion: "v1.2", Class: "Workflow",
		RawCWL: "::: not yaml at all :::", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create workflow wf-bad-cwl: %v", err)
	}

	mk := func(id string) time.Time { return now.Add(time.Duration(len(id)) * time.Second) }

	// 1. Valid submission (submitted fidelity).
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-valid", WorkflowID: "wf-good", WorkflowName: "enum-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-valid"),
		Inputs: map[string]any{"mode": "fast", "password": "hunter2"},
	}); err != nil {
		t.Fatalf("create sub-valid: %v", err)
	}

	// 2. Bad enum value -> would-be failure, secret password also wrong type.
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-bad-enum", WorkflowID: "wf-good", WorkflowName: "enum-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-bad-enum"),
		Inputs: map[string]any{"mode": "medium", "password": "hunter2"},
	}); err != nil {
		t.Fatalf("create sub-bad-enum: %v", err)
	}

	// 3. Second submission with the same bad enum value, to test grouping/counts.
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-bad-enum-2", WorkflowID: "wf-good", WorkflowName: "enum-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-bad-enum-2"),
		Inputs: map[string]any{"mode": "medium", "password": "hunter2"},
	}); err != nil {
		t.Fatalf("create sub-bad-enum-2: %v", err)
	}

	// 4. Child submission (has parent_task_id) -> skipped regardless of inputs.
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-child", WorkflowID: "wf-good", WorkflowName: "enum-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-child"),
		ParentTaskID: "task-123",
		Inputs:       map[string]any{"mode": "not-even-checked"},
	}); err != nil {
		t.Fatalf("create sub-child: %v", err)
	}

	// 5. Orphaned submission (workflow row missing).
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-orphan", WorkflowID: "wf-does-not-exist", WorkflowName: "gone",
		State: model.SubmissionState("FAILED"), CreatedAt: mk("sub-orphan"),
		Inputs: map[string]any{"mode": "fast"},
	}); err != nil {
		t.Fatalf("create sub-orphan: %v", err)
	}

	// 6. Submission against the unparseable workflow.
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-unparseable", WorkflowID: "wf-bad-cwl", WorkflowName: "broken-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-unparseable"),
		Inputs: map[string]any{"whatever": 1},
	}); err != nil {
		t.Fatalf("create sub-unparseable: %v", err)
	}

	// 7. Stored-fidelity submission: submitted_inputs will be cleared below
	// so Run must fall back to the `inputs` column and use it directly, and
	// that fallback should also be a would-be failure (bad enum).
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub-stored-fidelity", WorkflowID: "wf-good", WorkflowName: "enum-flow",
		State: model.SubmissionState("COMPLETED"), CreatedAt: mk("sub-stored-fidelity"),
		Inputs: map[string]any{"mode": "glacial", "password": "hunter2"},
	}); err != nil {
		t.Fatalf("create sub-stored-fidelity: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Force sub-stored-fidelity's submitted_inputs to empty, simulating a
	// pre-#239 row / a row where the snapshot column carries no data.
	rawExec(t, dbPath, `UPDATE submissions SET submitted_inputs = '{}' WHERE id = ?`, "sub-stored-fidelity")

	db := openTestDBReadOnly(t, dbPath)
	report, err := Run(ctx, db, dbPath, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Run("coverage counts", func(t *testing.T) {
		c := report.Coverage
		if c.Total != 7 {
			t.Errorf("Total = %d, want 7", c.Total)
		}
		if c.Child != 1 {
			t.Errorf("Child = %d, want 1", c.Child)
		}
		if c.Orphaned != 1 {
			t.Errorf("Orphaned = %d, want 1", c.Orphaned)
		}
		if c.Unparseable != 1 {
			t.Errorf("Unparseable = %d, want 1", c.Unparseable)
		}
		// Replayable = valid + 2 bad-enum (submitted fidelity) = 3; stored
		// fidelity fallback = 1.
		if c.ReplayableSubmitted != 3 {
			t.Errorf("ReplayableSubmitted = %d, want 3", c.ReplayableSubmitted)
		}
		if c.ReplayableStored != 1 {
			t.Errorf("ReplayableStored = %d, want 1", c.ReplayableStored)
		}
		if c.WouldFail != 3 {
			t.Errorf("WouldFail = %d, want 3 (2 submitted + 1 stored bad enum)", c.WouldFail)
		}
	})

	t.Run("unparseable workflow reported once with error", func(t *testing.T) {
		if len(report.UnparseableWorkflows) != 1 {
			t.Fatalf("UnparseableWorkflows = %d entries, want 1: %+v", len(report.UnparseableWorkflows), report.UnparseableWorkflows)
		}
		u := report.UnparseableWorkflows[0]
		if u.WorkflowID != "wf-bad-cwl" || u.AffectedSubmissions != 1 {
			t.Errorf("unexpected unparseable entry: %+v", u)
		}
	})

	t.Run("failure groups grouped and counted, no values echoed", func(t *testing.T) {
		var enumGroup *FailureGroup
		for i := range report.FailureGroups {
			g := &report.FailureGroups[i]
			if g.WorkflowName == "enum-flow" && g.InputID == "mode" {
				enumGroup = g
			}
			if strings.Contains(g.Message, "medium") || strings.Contains(g.Message, "glacial") || strings.Contains(g.Message, "hunter2") {
				t.Errorf("failure group message leaks a value: %q", g.Message)
			}
		}
		if enumGroup == nil {
			t.Fatalf("no failure group for enum-flow/mode: %+v", report.FailureGroups)
		}
		// sub-bad-enum, sub-bad-enum-2 (submitted fidelity, same message) and
		// sub-stored-fidelity (stored fidelity, same message pattern) should
		// all land in ONE group since the message never contains the value.
		if enumGroup.Count != 3 {
			t.Errorf("enum group Count = %d, want 3: %+v", enumGroup.Count, enumGroup)
		}
		if len(enumGroup.Examples) != 3 {
			t.Errorf("enum group Examples = %v, want 3 example ids", enumGroup.Examples)
		}
	})

	t.Run("example cap of 3 per group", func(t *testing.T) {
		for _, g := range report.FailureGroups {
			if len(g.Examples) > 3 {
				t.Errorf("group %+v has more than 3 examples", g)
			}
		}
	})
}

func TestRun_ExampleCapAboveThree(t *testing.T) {
	dbPath := newTestDB(t)
	st, err := store.NewSQLiteStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf-good", Name: "enum-flow", CWLVersion: "v1.2", Class: "Workflow",
		RawCWL: enumWorkflowCWL, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	for i := 0; i < 5; i++ {
		id := "sub-" + string(rune('a'+i))
		if err := st.CreateSubmission(ctx, &model.Submission{
			ID: id, WorkflowID: "wf-good", WorkflowName: "enum-flow",
			State: model.SubmissionState("COMPLETED"), CreatedAt: now.Add(time.Duration(i) * time.Second),
			Inputs: map[string]any{"mode": "bogus", "password": "x"},
		}); err != nil {
			t.Fatalf("create submission %s: %v", id, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db := openTestDBReadOnly(t, dbPath)
	report, err := Run(ctx, db, dbPath, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.FailureGroups) != 1 {
		t.Fatalf("FailureGroups = %d, want 1: %+v", len(report.FailureGroups), report.FailureGroups)
	}
	g := report.FailureGroups[0]
	if g.Count != 5 {
		t.Errorf("Count = %d, want 5", g.Count)
	}
	if len(g.Examples) != 3 {
		t.Errorf("Examples = %v, want exactly 3 (capped)", g.Examples)
	}
}

func TestRun_LimitAndSince(t *testing.T) {
	dbPath := newTestDB(t)
	st, err := store.NewSQLiteStore(dbPath, slog.Default())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf-good", Name: "enum-flow", CWLVersion: "v1.2", Class: "Workflow",
		RawCWL: enumWorkflowCWL, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	for i := 0; i < 4; i++ {
		id := "sub-" + string(rune('a'+i))
		if err := st.CreateSubmission(ctx, &model.Submission{
			ID: id, WorkflowID: "wf-good", WorkflowName: "enum-flow",
			State: model.SubmissionState("COMPLETED"), CreatedAt: base.Add(time.Duration(i) * time.Hour),
			Inputs: map[string]any{"mode": "fast", "password": "x"},
		}); err != nil {
			t.Fatalf("create submission %s: %v", id, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	db := openTestDBReadOnly(t, dbPath)

	t.Run("limit caps total", func(t *testing.T) {
		report, err := Run(ctx, db, dbPath, RunOptions{Limit: 2})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if report.Coverage.Total != 2 {
			t.Errorf("Total = %d, want 2", report.Coverage.Total)
		}
	})

	t.Run("since excludes earlier submissions", func(t *testing.T) {
		since := base.Add(2 * time.Hour)
		report, err := Run(ctx, db, dbPath, RunOptions{Since: &since})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		// sub-c (base+2h) and sub-d (base+3h) qualify.
		if report.Coverage.Total != 2 {
			t.Errorf("Total = %d, want 2", report.Coverage.Total)
		}
	})
}

func TestRefuseIfProduction(t *testing.T) {
	t.Run("literal production path is refused", func(t *testing.T) {
		_, err := refuseIfProduction(productionDBPath)
		if err == nil {
			t.Fatal("want refusal for production DB path, got nil error")
		}
	})

	t.Run("a copy elsewhere is allowed", func(t *testing.T) {
		dbPath := newTestDB(t)
		abs, err := refuseIfProduction(dbPath)
		if err != nil {
			t.Fatalf("unexpected refusal for a copy: %v", err)
		}
		if abs == "" {
			t.Fatal("want a resolved absolute path")
		}
	})
}
