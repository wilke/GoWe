package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/me/gowe/pkg/model"
)

// newCopySource returns a migrated in-memory SQLite store, whatever
// GOWE_TEST_POSTGRES_URL says: the source of CopyDatabase is always SQLite.
func newCopySource(t *testing.T) *SQLStore {
	t.Helper()
	st, err := NewSQLiteStore(":memory:", testLogger())
	if err != nil {
		t.Fatalf("open sqlite source: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate sqlite source: %v", err)
	}
	return st
}

// countRows returns the number of rows of table in st.
func countRows(t *testing.T, st *SQLStore, table string) int64 {
	t.Helper()
	var n int64
	if err := st.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestCopyTables_CoverSchema fails when Migrate creates a table that
// copyTables does not list (its rows would not be copied), when the list
// names a table Migrate does not create, or when a listed primary key is not
// the table's primary key.
func TestCopyTables_CoverSchema(t *testing.T) {
	st := newCopySource(t)
	ctx := context.Background()

	got, err := st.dialect.tableNames(ctx, st.db)
	if err != nil {
		t.Fatalf("tableNames: %v", err)
	}
	var want []string
	for _, ct := range copyTables {
		want = append(want, ct.name)
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tables created by Migrate = %v\ncopyTables lists    %v\n(add the new table to copyTables in copy.go, in foreign-key order)", got, want)
	}

	pos := map[string]int{}
	for i, ct := range copyTables {
		pos[ct.name] = i
	}
	if pos["users"] > pos["linked_providers"] {
		t.Error("users must be copied before linked_providers (foreign key)")
	}

	for _, ct := range copyTables {
		t.Run(ct.name, func(t *testing.T) {
			rows, err := st.db.QueryContext(ctx, "PRAGMA table_info("+ct.name+")")
			if err != nil {
				t.Fatalf("table_info: %v", err)
			}
			defer rows.Close()
			type pkCol struct {
				pos  int
				name string
			}
			var pks []pkCol
			for rows.Next() {
				var cid, notnull, pk int
				var name, ctype string
				var dflt *string
				if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
					t.Fatalf("scan table_info: %v", err)
				}
				if pk > 0 {
					pks = append(pks, pkCol{pk, strings.ToLower(name)})
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("table_info: %v", err)
			}
			sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
			var names []string
			for _, p := range pks {
				names = append(names, p.name)
			}
			if !reflect.DeepEqual(names, ct.pk) {
				t.Errorf("primary key = %v, copyTables says %v", names, ct.pk)
			}
		})
	}
}

// copyFixture holds what fillCopySource wrote.
type copyFixture struct {
	workflowID   string
	submissionID string
	stepID       string
	nulTaskID    string // stdout with a NUL byte and invalid UTF-8; old created_at
	hintsTaskID  string // runtime hints with a NUL character
	workerID     string
	workerKeyID  string
	blessingID   string
	svcTokenHash string // hash of the service token (it is looked up by it)
	username     string
	sessionID    string
	oldCreatedAt string
}

// fillCopySource writes at least one row into every table of src through the
// store's own methods, then rewrites one task's created_at to the
// variable-width form older versions wrote.
func fillCopySource(t *testing.T, src *SQLStore) copyFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	fx := copyFixture{oldCreatedAt: "2026-03-01T10:00:05Z"}

	wf := sampleWorkflow()
	wf.Labels = map[string]string{"team": "genomics", "tier": "gold"}
	if err := src.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	fx.workflowID = wf.ID

	sub := sampleSubmission(wf.ID)
	if err := src.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}
	fx.submissionID = sub.ID

	si := &model.StepInstance{
		ID: "si_copy-1", SubmissionID: sub.ID, StepID: "assemble",
		State: model.StepStateWaiting, Outputs: map[string]any{}, CreatedAt: now,
	}
	if err := src.CreateStepInstance(ctx, si); err != nil {
		t.Fatalf("create step instance: %v", err)
	}
	fx.stepID = si.ID

	nulTask := sampleTask(sub.ID)
	nulTask.ID = "task_copy-nul"
	nulTask.StepInstanceID = si.ID
	nulTask.Stdout = "out\x00put \xff\xfe end"
	if err := src.CreateTask(ctx, nulTask); err != nil {
		t.Fatalf("create task: %v", err)
	}
	fx.nulTaskID = nulTask.ID

	hintsTask := sampleTask(sub.ID)
	hintsTask.ID = "task_copy-hints"
	hintsTask.StepInstanceID = si.ID
	hintsTask.RuntimeHints = &model.RuntimeHints{DockerImage: "img\x00tag", WorkerGroup: "gpu", Cores: 2, RamMB: 4096}
	if err := src.CreateTask(ctx, hintsTask); err != nil {
		t.Fatalf("create task: %v", err)
	}
	fx.hintsTaskID = hintsTask.ID

	// One archived attempt of the hints task (clean text, fixed-width times:
	// nothing for the copy to fix or sanitise in task_attempts).
	if _, err := src.db.ExecContext(ctx, `UPDATE tasks SET stderr = 'boom', exit_code = 1, started_at = ? WHERE id = ?`,
		formatTime(now), hintsTask.ID); err != nil {
		t.Fatalf("give the hints task an attempt: %v", err)
	}
	archiveTask(t, src, hintsTask.ID, model.AttemptReasonRetry)

	w := sampleWorker()
	if err := src.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	fx.workerID = w.ID

	key := sampleWorkerKey("wk_copy-1", "gwk_copysecret123")
	if err := src.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("create worker key: %v", err)
	}
	fx.workerKeyID = key.ID

	svcRaw := "gowe_svc_" + strings.Repeat("k", 43)
	svc := sampleServiceToken("svt_copy-1", "model-router", svcRaw)
	svcExp := now.Add(time.Hour)
	svc.ExpiresAt = &svcExp
	if err := src.CreateServiceToken(ctx, svc); err != nil {
		t.Fatalf("create service token: %v", err)
	}
	if err := src.TouchServiceToken(ctx, svc.ID, now); err != nil {
		t.Fatalf("touch service token: %v", err)
	}
	fx.svcTokenHash = svc.TokenHash

	b := sampleBlessing("tb_copy-1", "sha256:copy")
	if err := src.CreateToolBlessing(ctx, b); err != nil {
		t.Fatalf("create blessing: %v", err)
	}
	fx.blessingID = b.ID

	if err := src.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{
		Group: "gpu", Users: []string{"alice@bvbrc"}, Admins: true, UpdatedBy: "admin", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create worker group policy: %v", err)
	}
	if err := src.SetServerSetting(ctx, "worker_groups.default_deny", "true", "admin"); err != nil {
		t.Fatalf("set server setting: %v", err)
	}

	user, err := src.GetOrCreateUser(ctx, "alice@bvbrc", model.ProviderBVBRC)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := src.LinkProvider(ctx, user.ID, model.ProviderMGRAST, "alice_mg"); err != nil {
		t.Fatalf("link provider: %v", err)
	}
	fx.username = user.Username

	sess := &model.Session{
		ID: "sess_copy-0123456789abcdef", UserID: user.ID, Username: user.Username, Role: "user",
		Token: "un=alice|sig=x", TokenExp: time.Unix(1800000000, 0),
		CreatedAt: time.Unix(1790000000, 0), ExpiresAt: time.Unix(1790086400, 0),
	}
	if err := src.CreateSession(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	fx.sessionID = sess.ID

	lv := &model.LabelVocabulary{ID: "lv_copy-1", Key: "project", Value: "copy", Description: "d", Color: "blue", CreatedAt: now}
	if err := src.CreateLabelVocabulary(ctx, lv); err != nil {
		t.Fatalf("create label vocabulary: %v", err)
	}

	if _, err := src.db.ExecContext(ctx, `UPDATE tasks SET created_at = ? WHERE id = ?`, fx.oldCreatedAt, nulTask.ID); err != nil {
		t.Fatalf("rewrite created_at: %v", err)
	}

	for _, ct := range copyTables {
		if countRows(t, src, ct.name) == 0 {
			t.Fatalf("fixture leaves table %s empty", ct.name)
		}
	}
	return fx
}

// checkCopy runs CopyDatabase from src into dst and checks the report and,
// through dst's API, the copied objects. pg says whether dst is PostgreSQL
// (text is then sanitised).
func checkCopy(t *testing.T, src, dst *SQLStore, fx copyFixture, pg bool) {
	t.Helper()
	ctx := context.Background()

	progressed := map[string]int64{}
	report, err := CopyDatabase(ctx, src, dst, func(table string, rows int64) { progressed[table] = rows })
	if err != nil {
		t.Fatalf("CopyDatabase: %v", err)
	}

	if len(report.Tables) != len(copyTables) {
		t.Fatalf("report has %d tables, want %d", len(report.Tables), len(copyTables))
	}
	var total int64
	for i, tr := range report.Tables {
		if tr.Table != copyTables[i].name {
			t.Errorf("report table %d = %s, want %s", i, tr.Table, copyTables[i].name)
		}
		want := countRows(t, src, tr.Table)
		if tr.Rows != want {
			t.Errorf("%s: report says %d rows, source has %d", tr.Table, tr.Rows, want)
		}
		if got := countRows(t, dst, tr.Table); got != want {
			t.Errorf("%s: destination has %d rows, source %d", tr.Table, got, want)
		}
		if progressed[tr.Table] != tr.Rows {
			t.Errorf("%s: last progress %d, want %d", tr.Table, progressed[tr.Table], tr.Rows)
		}
		wantFixed, wantSanitised := int64(0), int64(0)
		if tr.Table == "tasks" {
			wantFixed = 1 // the old created_at
			if pg {
				wantSanitised = 2 // stdout of the NUL task, runtime_hints of the hints task
			}
		}
		if tr.TimestampsFixed != wantFixed {
			t.Errorf("%s: TimestampsFixed = %d, want %d", tr.Table, tr.TimestampsFixed, wantFixed)
		}
		if tr.TextSanitised != wantSanitised {
			t.Errorf("%s: TextSanitised = %d, want %d", tr.Table, tr.TextSanitised, wantSanitised)
		}
		total += tr.Rows
	}
	if report.TotalRows() != total {
		t.Errorf("TotalRows = %d, want %d", report.TotalRows(), total)
	}

	var raw string
	if err := dst.db.QueryRowContext(ctx, `SELECT created_at FROM tasks WHERE id = ?`, fx.nulTaskID).Scan(&raw); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if raw != "2026-03-01T10:00:05.000000000Z" {
		t.Errorf("copied created_at = %q, want the fixed-width form", raw)
	}

	same := func(what string, want, got any) {
		t.Helper()
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s differs after the copy:\nsource      %+v\ndestination %+v", what, want, got)
		}
	}

	wfS, err := src.GetWorkflow(ctx, fx.workflowID)
	if err != nil {
		t.Fatalf("source workflow: %v", err)
	}
	wfD, err := dst.GetWorkflow(ctx, fx.workflowID)
	if err != nil {
		t.Fatalf("destination workflow: %v", err)
	}
	same("workflow", wfS, wfD)
	if wfD == nil || wfD.Labels["tier"] != "gold" {
		t.Errorf("workflow labels not copied: %+v", wfD)
	}

	subS, err := src.GetSubmission(ctx, fx.submissionID)
	if err != nil {
		t.Fatalf("source submission: %v", err)
	}
	subD, err := dst.GetSubmission(ctx, fx.submissionID)
	if err != nil {
		t.Fatalf("destination submission: %v", err)
	}
	// GetSubmission embeds the tasks; they are compared one by one below,
	// where the text PostgreSQL cannot store is taken into account.
	subS.Tasks, subD.Tasks = nil, nil
	same("submission", subS, subD)

	siS, err := src.GetStepInstance(ctx, fx.stepID)
	if err != nil {
		t.Fatalf("source step instance: %v", err)
	}
	siD, err := dst.GetStepInstance(ctx, fx.stepID)
	if err != nil {
		t.Fatalf("destination step instance: %v", err)
	}
	same("step instance", siS, siD)

	for _, id := range []string{fx.nulTaskID, fx.hintsTaskID} {
		tS, err := src.GetTask(ctx, id)
		if err != nil || tS == nil {
			t.Fatalf("source task %s: %v %v", id, tS, err)
		}
		tD, err := dst.GetTask(ctx, id)
		if err != nil || tD == nil {
			t.Fatalf("destination task %s: %v %v", id, tD, err)
		}
		if pg {
			// What PostgreSQL can store: the sanitised text, and the JSON
			// NUL escape replaced by U+FFFD.
			tS.Stdout, _ = pgSanitizeText(tS.Stdout)
			if tS.RuntimeHints != nil {
				tS.RuntimeHints.DockerImage = strings.ReplaceAll(tS.RuntimeHints.DockerImage, "\x00", replacement)
			}
		}
		same("task "+id, tS, tD)
	}
	if pg {
		tD, err := dst.GetTask(ctx, fx.nulTaskID)
		if err != nil {
			t.Fatalf("destination task: %v", err)
		}
		if want := "output " + replacement + " end"; tD.Stdout != want {
			t.Errorf("sanitised stdout = %q, want %q", tD.Stdout, want)
		}
		// The JSON helpers must be able to read every copied runtime_hints.
		var n int
		if err := dst.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tasks WHERE `+dst.dialect.jsonText("runtime_hints", "docker_image")+` IS NOT NULL`).Scan(&n); err != nil {
			t.Fatalf("query runtime_hints with the JSON helpers: %v", err)
		}
		if n != 1 {
			t.Errorf("%d tasks with a docker_image, want 1", n)
		}
	}

	wS, err := src.GetWorker(ctx, fx.workerID)
	if err != nil {
		t.Fatalf("source worker: %v", err)
	}
	wD, err := dst.GetWorker(ctx, fx.workerID)
	if err != nil {
		t.Fatalf("destination worker: %v", err)
	}
	same("worker", wS, wD)

	kS, err := src.GetWorkerKeyByID(ctx, fx.workerKeyID)
	if err != nil {
		t.Fatalf("source worker key: %v", err)
	}
	kD, err := dst.GetWorkerKeyByID(ctx, fx.workerKeyID)
	if err != nil {
		t.Fatalf("destination worker key: %v", err)
	}
	same("worker key", kS, kD)

	stS, err := src.GetServiceTokenByHash(ctx, fx.svcTokenHash)
	if err != nil || stS == nil {
		t.Fatalf("source service token: %v %v", stS, err)
	}
	stD, err := dst.GetServiceTokenByHash(ctx, fx.svcTokenHash)
	if err != nil || stD == nil {
		t.Fatalf("destination service token: %v %v", stD, err)
	}
	same("service token", stS, stD)
	if stD.LastUsedAt == nil || stD.ExpiresAt == nil {
		t.Errorf("service token times not copied: %+v", stD)
	}

	bS, err := src.GetToolBlessing(ctx, fx.blessingID)
	if err != nil {
		t.Fatalf("source blessing: %v", err)
	}
	bD, err := dst.GetToolBlessing(ctx, fx.blessingID)
	if err != nil {
		t.Fatalf("destination blessing: %v", err)
	}
	same("tool blessing", bS, bD)

	gpS, err := src.ListWorkerGroupPolicies(ctx)
	if err != nil {
		t.Fatalf("source worker group policies: %v", err)
	}
	gpD, err := dst.ListWorkerGroupPolicies(ctx)
	if err != nil {
		t.Fatalf("destination worker group policies: %v", err)
	}
	same("worker group policies", gpS, gpD)

	for _, s := range []*SQLStore{src, dst} {
		if v, found, err := s.GetServerSetting(ctx, "worker_groups.default_deny"); err != nil || !found || v != "true" {
			t.Errorf("server setting = %q, %v, %v; want \"true\" in source and destination", v, found, err)
		}
	}

	uS, err := src.GetUser(ctx, fx.username)
	if err != nil {
		t.Fatalf("source user: %v", err)
	}
	uD, err := dst.GetUser(ctx, fx.username)
	if err != nil {
		t.Fatalf("destination user: %v", err)
	}
	same("user", uS, uD)
	if uD == nil || len(uD.LinkedProviders) != 1 || uD.LinkedProviders[0].Username != "alice_mg" {
		t.Errorf("linked provider not copied: %+v", uD)
	}

	sS, err := src.GetSession(ctx, fx.sessionID)
	if err != nil {
		t.Fatalf("source session: %v", err)
	}
	sD, err := dst.GetSession(ctx, fx.sessionID)
	if err != nil {
		t.Fatalf("destination session: %v", err)
	}
	same("session", sS, sD)

	lvS, err := src.ListLabelVocabulary(ctx)
	if err != nil {
		t.Fatalf("source label vocabulary: %v", err)
	}
	lvD, err := dst.ListLabelVocabulary(ctx)
	if err != nil {
		t.Fatalf("destination label vocabulary: %v", err)
	}
	same("label vocabulary", lvS, lvD)
}

// TestCopyDatabase_RoundTripPostgres copies a SQLite database holding a row
// in every table into PostgreSQL.
func TestCopyDatabase_RoundTripPostgres(t *testing.T) {
	if os.Getenv(postgresTestURLEnv) == "" {
		t.Skipf("%s is not set", postgresTestURLEnv)
	}
	src := newCopySource(t)
	fx := fillCopySource(t, src)
	dst := testStore(t)
	if !isPostgres(dst) {
		t.Fatal("testStore did not return a PostgreSQL store")
	}
	checkCopy(t, src, dst, fx, true)
}

// TestCopyDatabase_RoundTripSQLite runs the same copy into another SQLite
// database, so the copy and verification code runs without PostgreSQL.
func TestCopyDatabase_RoundTripSQLite(t *testing.T) {
	src := newCopySource(t)
	fx := fillCopySource(t, src)
	dst := newCopySource(t)
	checkCopy(t, src, dst, fx, false)
}

// TestCopyDatabase_TargetNotEmpty: a destination with a row is refused
// before anything is written, and keeps exactly what it had.
func TestCopyDatabase_TargetNotEmpty(t *testing.T) {
	ctx := context.Background()
	src := newCopySource(t)
	fillCopySource(t, src)
	dst := testStore(t)

	wf := sampleWorkflow()
	wf.ID = "wf_already-there"
	if err := dst.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow in the destination: %v", err)
	}
	before := map[string]int64{}
	for _, ct := range copyTables {
		before[ct.name] = countRows(t, dst, ct.name)
	}

	_, err := CopyDatabase(ctx, src, dst, nil)
	if err == nil {
		t.Fatal("CopyDatabase into a non-empty database succeeded")
	}
	if !strings.Contains(err.Error(), "target database is not empty (table workflows has 1 rows)") {
		t.Errorf("error = %v, want the not-empty error naming workflows", err)
	}
	for _, ct := range copyTables {
		if got := countRows(t, dst, ct.name); got != before[ct.name] {
			t.Errorf("%s: %d rows after the refused copy, %d before", ct.name, got, before[ct.name])
		}
	}
	if got, err := dst.GetWorkflow(ctx, wf.ID); err != nil || got == nil {
		t.Errorf("existing workflow gone: %v %v", got, err)
	}
}

// TestCopyDatabase_SchemaMismatch: a source that is not at the current schema
// is refused with the table, the column and the way out.
func TestCopyDatabase_SchemaMismatch(t *testing.T) {
	ctx := context.Background()
	src := newCopySource(t)
	if _, err := src.db.ExecContext(ctx, `ALTER TABLE tasks DROP COLUMN charged_gpus`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	dst := testStore(t)

	_, err := CopyDatabase(ctx, src, dst, nil)
	if err == nil {
		t.Fatal("CopyDatabase with an old source schema succeeded")
	}
	for _, want := range []string{"table tasks", "charged_gpus", "missing in the source", "--migrate-source"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q: %v", want, err)
		}
	}
	for _, ct := range copyTables {
		if n := countRows(t, dst, ct.name); n != 0 {
			t.Errorf("%s: %d rows in the destination after the refused copy", ct.name, n)
		}
	}
}

// TestCopyDatabase_UnknownSourceTable: a table the copy does not know is
// refused instead of being left behind.
func TestCopyDatabase_UnknownSourceTable(t *testing.T) {
	ctx := context.Background()
	src := newCopySource(t)
	if _, err := src.db.ExecContext(ctx, `CREATE TABLE operator_notes (id TEXT PRIMARY KEY, note TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	dst := testStore(t)
	_, err := CopyDatabase(ctx, src, dst, nil)
	if err == nil || !strings.Contains(err.Error(), `"operator_notes"`) {
		t.Fatalf("err = %v, want an error naming operator_notes", err)
	}
}

func TestCompareRows(t *testing.T) {
	cols := []string{"id", "n", "text", "opt"}
	tests := []struct {
		name    string
		want    []any
		got     []any
		wantCol string // "" = equal
	}{
		{name: "equal", want: []any{"a", int64(1), "x", nil}, got: []any{"a", int64(1), "x", nil}},
		{name: "bytes equal string", want: []any{[]byte("a"), int64(1), "x", nil}, got: []any{"a", int64(1), []byte("x"), nil}},
		{name: "integral float equals int", want: []any{"a", float64(3), "x", nil}, got: []any{"a", int64(3), "x", nil}},
		{name: "int differs", want: []any{"a", int64(1), "x", nil}, got: []any{"a", int64(2), "x", nil}, wantCol: "n"},
		{name: "text differs", want: []any{"a", int64(1), "secret-1", nil}, got: []any{"a", int64(1), "secret-2", nil}, wantCol: "text"},
		{name: "NULL is not empty text", want: []any{"a", int64(1), "x", nil}, got: []any{"a", int64(1), "x", ""}, wantCol: "opt"},
		{name: "integer is not text", want: []any{"a", int64(1), "x", nil}, got: []any{"a", "1", "x", nil}, wantCol: "n"},
		{name: "first difference wins", want: []any{"a", int64(1), "x", nil}, got: []any{"b", int64(2), "y", "z"}, wantCol: "id"},
		{name: "short row", want: []any{"a", int64(1), "x", nil}, got: []any{"a", int64(1), "x"}, wantCol: "(number of columns)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			col, ok := compareRows(cols, tc.want, tc.got)
			if ok != (tc.wantCol == "") || col != tc.wantCol {
				t.Errorf("compareRows = %q, %v; want %q", col, ok, tc.wantCol)
			}
		})
	}
}

func TestTransformCopyValue(t *testing.T) {
	ts := copyColumn{name: "created_at", timestamp: true}
	doc := copyColumn{name: "runtime_hints", jsonDoc: true}
	plain := copyColumn{name: "stdout"}
	pg, lite := postgresDialect{}, sqliteDialect{}
	tests := []struct {
		name          string
		d             dialect
		col           copyColumn
		in            any
		want          any
		wantFixed     bool
		wantSanitised bool
	}{
		{name: "old timestamp fixed", d: pg, col: ts, in: "2026-03-01T10:00:05Z", want: "2026-03-01T10:00:05.000000000Z", wantFixed: true},
		{name: "offset timestamp to UTC", d: lite, col: ts, in: "2026-03-01T12:00:05.5+02:00", want: "2026-03-01T10:00:05.500000000Z", wantFixed: true},
		{name: "fixed-width timestamp kept", d: pg, col: ts, in: "2026-03-01T10:00:05.000000000Z", want: "2026-03-01T10:00:05.000000000Z"},
		{name: "unparsable timestamp kept", d: pg, col: ts, in: "yesterday", want: "yesterday"},
		{name: "empty timestamp kept", d: pg, col: ts, in: "", want: ""},
		{name: "timestamp as bytes", d: pg, col: ts, in: []byte("2026-03-01T10:00:05Z"), want: "2026-03-01T10:00:05.000000000Z", wantFixed: true},
		{name: "integer timestamp untouched", d: pg, col: ts, in: int64(1790000000), want: int64(1790000000)},
		{name: "NULL untouched", d: pg, col: ts, in: nil, want: nil},
		{name: "NUL and invalid UTF-8 on postgres", d: pg, col: plain, in: "a\x00b\xff", want: "ab" + replacement, wantSanitised: true},
		{name: "NUL kept on sqlite", d: lite, col: plain, in: "a\x00b\xff", want: "a\x00b\xff"},
		{name: "JSON NUL escape in a parsed document", d: pg, col: doc, in: `{"a":"x\u0000y"}`, want: `{"a":"x` + `\u` + `fffd` + `y"}`, wantSanitised: true},
		{name: "JSON NUL escape kept on sqlite", d: lite, col: doc, in: `{"a":"x\u0000y"}`, want: `{"a":"x\u0000y"}`},
		{name: "JSON NUL escape in plain text kept", d: pg, col: plain, in: `{"a":"x\u0000y"}`, want: `{"a":"x\u0000y"}`},
		{name: "invalid bytes become sanitised text", d: pg, col: plain, in: []byte("b\x00\xff"), want: "b" + replacement, wantSanitised: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fixed, sanitised := transformCopyValue(tc.d, tc.col, tc.in)
			if !reflect.DeepEqual(got, tc.want) || fixed != tc.wantFixed || sanitised != tc.wantSanitised {
				t.Errorf("transformCopyValue = %#v, %v, %v; want %#v, %v, %v", got, fixed, sanitised, tc.want, tc.wantFixed, tc.wantSanitised)
			}
		})
	}
}

// TestCopyErrors_NoValues: the row reference of a table whose key is a
// credential shows no key, and driver errors lose their message.
func TestCopyErrors_NoValues(t *testing.T) {
	sessions := &copyPlan{table: copyTable{name: "sessions", pk: []string{"id"}, secretKey: true}, pkIdx: []int{0}}
	if ref := rowRef(sessions, []any{"sess-cookie-value"}, 7); strings.Contains(ref, "sess-cookie-value") || !strings.Contains(ref, "row 7") {
		t.Errorf("session row reference = %q", ref)
	}
	lp := &copyPlan{table: copyTable{name: "linked_providers", pk: []string{"user_id", "provider"}}, pkIdx: []int{0, 1}}
	if ref := rowRef(lp, []any{"user_1", "bvbrc", "secret"}, 1); ref != `key user_id="user_1", provider="bvbrc"` {
		t.Errorf("linked_providers row reference = %q", ref)
	}

	pgErr := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type bigint: "tok-123"`, ColumnName: "token_exp"}
	msg := safeDriverError(pgErr).Error()
	if strings.Contains(msg, "tok-123") || !strings.Contains(msg, "22P02") || !strings.Contains(msg, "token_exp") {
		t.Errorf("safeDriverError(PgError) = %q", msg)
	}
	if err := safeDriverError(context.Canceled); err != context.Canceled {
		t.Errorf("safeDriverError(context.Canceled) = %v", err)
	}
}

// TestCopyDatabase_VerificationFailureRollsBack changes the destination
// between the insert and the verification pass, the way a faulty copy would
// look, and expects the copy to fail and to leave the destination empty.
func TestCopyDatabase_VerificationFailureRollsBack(t *testing.T) {
	tests := []struct {
		name    string
		tamper  string // run in the destination transaction before verification
		wantErr string
	}{
		{"value changed", `UPDATE tasks SET step_id = 'tampered' WHERE id = ?`, "step_id"},
		{"row missing", `DELETE FROM tasks WHERE id = ?`, "table tasks"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			src := newCopySource(t)
			fx := fillCopySource(t, src)
			dst := testStore(t)

			copyBeforeVerifyHook = func(ctx context.Context, tx *sqlTx) error {
				_, err := tx.ExecContext(ctx, tc.tamper, fx.hintsTaskID)
				return err
			}
			t.Cleanup(func() { copyBeforeVerifyHook = nil })

			_, err := CopyDatabase(ctx, src, dst, nil)
			if err == nil {
				t.Fatal("CopyDatabase succeeded although the destination differs from the source")
			}
			if !strings.Contains(err.Error(), "verify") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want a verification error mentioning %q", err, tc.wantErr)
			}
			for _, table := range copyTables {
				var n int
				if err := dst.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table.name).Scan(&n); err != nil {
					t.Fatalf("count %s: %v", table.name, err)
				}
				if n != 0 {
					t.Errorf("%s has %d rows after a failed copy, want 0", table.name, n)
				}
			}
		})
	}
}

// TestOpenSQLiteReadOnly: a store opened read-only reads a WAL-mode file and
// cannot change it.
func TestOpenSQLiteReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gowe.db")
	rw, err := NewSQLiteStore(path, testLogger())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := rw.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := rw.CreateWorkflow(ctx, sampleWorkflow()); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	ro, err := OpenSQLiteReadOnly(path, testLogger())
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer ro.Close()
	if wf, err := ro.GetWorkflow(ctx, sampleWorkflow().ID); err != nil || wf == nil {
		t.Fatalf("read through the read-only store: %v %v", wf, err)
	}
	other := sampleWorkflow()
	other.ID = "wf_must-not-be-written"
	if err := ro.CreateWorkflow(ctx, other); err == nil {
		t.Error("a write through the read-only store succeeded")
	}
	ro.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the database file changed although it was opened read-only")
	}
}
